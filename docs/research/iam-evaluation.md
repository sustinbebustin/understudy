# IAM policy evaluation: primary-source research

Scope: AWS's documented IAM policy evaluation logic as an implementable specification for ROADMAP item 5 (IAM enforcement): the decision procedure across identity policies, resource policies, permissions boundaries, session policies, role trust policies and KMS key policies; same-account versus cross-account rules; the root user and service principals; every condition operator, set operator and policy variable rule; the global condition keys and which understudy can supply; every determinable worked example in the IAM User Guide and the KMS Developer Guide transcribed as test vectors; and the layout of the AWS Managed Policy Reference Guide that the catalogue generator of ADR 0016 (managed policies amendment) reads. Sources: the IAM User Guide, the STS and IAM API References, the KMS Developer Guide, the S3 API Reference and the AWS Managed Policy Reference Guide on docs.aws.amazon.com (read as the `.md` mirrors, flow charts read as images), the Smithy models pinned at `aws/api-models-aws@1cfdfb4bb9ea858e65623e66d73af04bec353201`, `hashicorp/terraform-provider-aws` v6.68.0, `hashicorp/awspolicyequivalence` v1.8.0, and LocalStack's archived test snapshots (localstack@8b9a79f). All retrieved 2026-10-25.

## 0. How to read this

### 0.1 Sources

Short labels used in the vector tables (section 9). Every page was read on 2026-10-25; the `.md` mirror at the same path was the text read.

| Label | URL |
|---|---|
| S-EVAL | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_evaluation-logic.html |
| S-CTX | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_evaluation-logic_policy-eval-reqcontext.html |
| S-ENF | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_evaluation-logic_policy-eval-denyallow.html |
| S-ONE | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_evaluation-logic_policy-eval-basics.html |
| S-XACCT | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_evaluation-logic-cross-account.html |
| S-DENY | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_evaluation-logic_AccessPolicyLanguage_Interplay.html |
| S-GRAMMAR | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_grammar.html |
| S-VERSION | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_version.html |
| S-PRINCIPAL | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_principal.html |
| S-NOTPRIN | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_notprincipal.html |
| S-ACTION | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_action.html |
| S-NOTACTION | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_notaction.html |
| S-RESOURCE | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_resource.html |
| S-NOTRES | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_notresource.html |
| S-COND | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_condition.html |
| S-OPS | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_condition_operators.html |
| S-MULTI | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_condition-logic-multiple-context-keys-or-values.html |
| S-SETS | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_condition-single-vs-multi-valued-context-keys.html |
| S-MVEX | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_condition_examples-multi-valued-context-keys.html |
| S-SVEX | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_condition_examples-single-valued-context-keys.html |
| S-VARS | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_variables.html |
| S-TYPES | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_datatypes.html |
| S-KEYS | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_condition-keys.html |
| S-POLICIES | https://docs.aws.amazon.com/IAM/latest/UserGuide/access_policies.html |
| S-BOUND | https://docs.aws.amazon.com/IAM/latest/UserGuide/access_policies_boundaries.html |
| S-XRES | https://docs.aws.amazon.com/IAM/latest/UserGuide/access_policies-cross-account-resource-access.html |
| S-ARPERM | https://docs.aws.amazon.com/IAM/latest/UserGuide/id_credentials_temp_control-access_assumerole.html |
| S-STSCMP | https://docs.aws.amazon.com/IAM/latest/UserGuide/id_credentials_sts-comparison.html |
| S-TAGS | https://docs.aws.amazon.com/IAM/latest/UserGuide/id_session-tags.html |
| S-ABAC | https://docs.aws.amazon.com/IAM/latest/UserGuide/access_tags.html |
| S-IDS | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_identifiers.html |
| S-ARNS | https://docs.aws.amazon.com/IAM/latest/UserGuide/reference-arns.html |
| S-ROOT | https://docs.aws.amazon.com/IAM/latest/UserGuide/id_root-user.html |
| S-DEPUTY | https://docs.aws.amazon.com/IAM/latest/UserGuide/confused-deputy.html |
| S-AD | https://docs.aws.amazon.com/IAM/latest/UserGuide/troubleshoot_access-denied.html |
| S-SIM | https://docs.aws.amazon.com/IAM/latest/UserGuide/access_policies_testing-policies.html |
| S-SWITCH | https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_use_permissions-to-switch.html |
| STS-AR | https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRole.html |
| IAM-SIM | https://docs.aws.amazon.com/IAM/latest/APIReference/API_SimulateCustomPolicy.html |
| K-POL | https://docs.aws.amazon.com/kms/latest/developerguide/key-policies.html |
| K-DEF | https://docs.aws.amazon.com/kms/latest/developerguide/key-policy-default.html |
| K-OVR | https://docs.aws.amazon.com/kms/latest/developerguide/key-policy-overview.html |
| K-DET | https://docs.aws.amazon.com/kms/latest/developerguide/determining-access-key-policy.html |
| K-XACCT | https://docs.aws.amazon.com/kms/latest/developerguide/key-policy-modifying-external-accounts.html |
| K-EVAL | https://docs.aws.amazon.com/kms/latest/developerguide/policy-evaluation.html |
| K-CTL | https://docs.aws.amazon.com/kms/latest/developerguide/control-access.html |
| S3-PBP | https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketPolicy.html |
| M-LLMS | https://docs.aws.amazon.com/aws-managed-policy/latest/reference/llms.txt |
| M-FULL | https://docs.aws.amazon.com/aws-managed-policy/latest/reference/full.md |
| ROBOTS | https://docs.aws.amazon.com/robots.txt |

### 0.2 Labels and markers

- Evidence labels follow `internal/evidence`: aws-doc (stated in the pages above), third-party-recorded (a LocalStack snapshot whose test has a `last_validated_date` in the matching `*.validation.json`), inferred (reasoning from the sources, stated as such). Terraform and awspolicyequivalence source is client source, cited by file and line.
- UNVERIFIED marks a behavior no source fixes; each carries the id of the recording case in section 13 that settles it.
- "Same account" means the principal's account equals the account that owns the resource; otherwise the request is cross-account (S-XACCT `#policy-eval-cross-account`).

## 1. Findings that change the design

1. **The form of the principal a same-account resource policy names decides which other policy types can still veto it.** A grant to a role session ARN, an IAM user ARN or a federated-user ARN is final (only an explicit deny overrides it); a grant to a role ARN still needs the permissions boundary and the session policy to allow; a grant to the account needs the identity policy, the boundary and the session policy (S-ENF, S-ONE flow charts; section 2.3). A single-document `Allows` cannot express this: the evaluator must know which statement form matched.
2. **IAM role trust policies and KMS key policies must allow explicitly.** For every other resource an Allow in either the identity policy or the resource policy is enough within one account (S-ENF "Resource-based policies"); for a role and a KMS key the flow chart ends in implicit deny when the resource policy does not allow (sections 3.1, 4.1). A key policy statement naming the account does not grant; it only lets IAM policies grant (K-DEF `#key-policy-default-allow-root-enable-iam`).
3. **Cross-account is two independent evaluations, both of which must allow.** The trusted side always needs an identity-policy Allow, even when the resource policy names the exact role or session (S-XACCT, cross-account flow chart; section 2.4).
4. **An absent condition key is false for positive operators and true for negated ones, ForAllValues and IfExists.** `Null` tests presence; ForAnyValue on an absent key is false (S-OPS top box, `#Conditions_IfExists`, `#Conditions_Null`; S-SETS `#reference_policies_condition-forallvalues`, `#reference_policies_condition-foranyvalue`; truth table in 6.14). policydoc today treats an absent key as "assumed to hold" (`internal/policydoc/doc.go`); enforcement must not.
5. **Policy variables exist only in `2012-10-17` documents** (an absent `Version` included); a variable with no value makes positive string operators fail, negated ones succeed, and a Resource containing it match nothing (S-VERSION, S-VARS `#policy-vars-no-value`; section 7).
6. **The root user has full access in its own account but is still bound by explicit denies, by KMS key policies and by trust policies, cannot call AssumeRole, and always keeps S3 bucket-policy management** (S-ENF, K-POL, S-STSCMP, S3-PBP; section 2.6). understudy resolves every unminted key to root (ADR 0012), so with root's full access Terraform keeps working under enforcement.
7. **Denials have one documented message grammar but per-service wire codes.** `User: <arn> is not authorized to perform: <action> [on resource: <arn>] because no <type> policy allows the <action> action` or `... with an explicit deny in a <type> policy[: <policy arn>]` (S-AD `#access-denied-error-examples`). Terraform retries on substrings of it (tf-compute.md 8, tf-messaging.md 3.3). The models declare the denial shape only for S3, SNS, CloudWatch Logs and EventBridge (section 2.11).
8. **IAM's `SimulateCustomPolicy` is AWS's evaluator, free, with typed context entries** (IAM-SIM; `com.amazonaws.iam#ContextKeyTypeEnum`). Sixteen of the thirty-four UNVERIFIED items below are settled by simulator calls and one (IAM-EVAL-17) by `GetContextKeysForCustomPolicy`, all of which cost nothing; if understudy binds the same operation (12.2), every capture doubles as a parity test.
9. **The managed-policy catalogue is one request, not 1,608.** robots.txt sets `Crawl-delay: 5`; the guide's `llms.txt` lists 1,608 policy pages and a single `full.md` (8,326,656 bytes) carrying every page with the same anchors (section 10). Fetching the pages one by one takes about 2 h 14 min at the crawl delay.

## 2. The evaluation flow

### 2.1 Request context and policy types

- AWS authenticates, builds the request context, then evaluates every applicable policy (S-EVAL). The context holds the principal (and the policies associated with it), the actions, the resources, resource data such as tags, and environment data such as IP address, user agent, TLS status and time (S-CTX). Policies are matched by principal, by action on the resource, and by condition (S-CTX `#reqcontext_usage`).
- AWS documents nine policy types: identity-based, resource-based, VPC endpoint policies, permissions boundaries, SCPs, RCPs, ACLs, RAM resource shares and session policies (S-POLICIES). Declarative policies are not in the request context (S-CTX, Note).
- understudy has identity policies (user and role, inline and managed; IAM groups are unbound, so no group policies, `docs/coverage.md` iam), permissions boundaries (user and role), session policies (`AssumeRole` `Policy` and `PolicyArns`), resource policies (SQS queue policy, SNS topic policy, S3 bucket policy, Lambda function policy, KMS key policy, Secrets Manager resource policy) and role trust policies. It has no SCPs, RCPs, VPC endpoint policies or RAM shares. S3 ACLs, where stored, are a separate mechanism that AWS applies only across accounts ("ACLs cannot be used to control access for a principal within the same account", S-POLICIES `#policies_acl`); this note does not specify them.
- Within one policy, statements combine with a logical OR, and so do several policies of the same type that apply to a request (S-POLICIES `#policies-introduction`).
- A request that touches several resources is evaluated against each: "The entire statement is evaluated against every resource that is required" (S-OPS `#Conditions_IfExists`). The request is allowed only when every (action, resource) pair it needs is allowed (inferred from the same text).

### 2.2 Single-account decision procedure

The text of S-ENF and the two flow charts of S-ONE (`#policy-eval-basics-single-account-role`, `#policy-eval-basics-single-account-user`, read as images) give this procedure. Every decision starts as deny.

1. **Explicit deny.** Gather every applicable policy: SCPs, RCPs, resource policies, identity policies, permissions boundaries and session policies. If any applicable statement with `Effect: Deny` matches, the decision is explicit deny (S-ENF "Deny evaluation").
2. **RCPs, then SCPs.** If the resource's account has an applicable RCP and no Allow exists at every level from the organization root to the account, implicit deny; same for SCPs and the principal's account (S-ENF; flow charts). understudy has neither (2.5).
3. **Resource policy.** If a resource policy statement allows the request, what happens next depends on which principal form matched (2.3). If no resource policy allows it and the resource is an IAM role (trust policy) or a KMS key, implicit deny (flow chart diamond "Is the resource an IAM role or KMS key?"). Otherwise continue.
4. **Identity policies.** If the principal has no identity policy, or none allows the request, implicit deny (S-ENF "Identity-based policies").
5. **Permissions boundary.** If the entity the principal uses has a boundary that does not allow the request, implicit deny (S-ENF "IAM permissions boundaries").
6. **Session policy.** If the principal is not a session principal, allow. For a session principal with no session policy passed, "a default session policy is created" and the decision is allow; if a session policy is present and does not allow, implicit deny (S-ENF "Session policies").

### 2.3 What a same-account resource policy grant is worth, by principal form

| Statement names | Caller | Still required after the resource-policy Allow | Source |
|---|---|---|---|
| Role session ARN `arn:aws:sts::<acct>:assumed-role/<role>/<session>` | that session | nothing ("not limited by an implicit deny in an identity-based policy, a permissions boundary, or session policy") | S-ENF "IAM role session"; S-BOUND `#access_policies_boundaries-eval-logic` |
| IAM user ARN | that user | nothing ("not limited by an implicit deny in an identity-based policy or permissions boundary") | S-ENF "IAM user" |
| Federated-user ARN `arn:aws:sts::<acct>:federated-user/<name>` | that session | nothing | S-ENF "AWS STS federated user principal" |
| IAM user ARN of the user who federated | that user's federated session | boundary and session policy | S-ENF, same item |
| Role ARN (in `Principal` or in `aws:PrincipalArn`) | a session of that role | boundary and session policy; not the identity policy (the flow chart goes from "Allows the role of the calling principal" to the boundary check) | S-ENF "IAM role"; role flow chart |
| `"*"` with `aws:PrincipalArn` naming the role | a session of that role | nothing, "unless the identity-based policies contain an explicit deny" | S-ENF "IAM role"; S-PRINCIPAL `#principal-roles` |
| `"*"` with no principal condition | user or role session in the account | "For IAM users and role principals within your account, no other permissions are required" (S-PRINCIPAL `#principal-anonymous`); whether a role session's boundary still limits it is UNVERIFIED (IAM-EVAL-19); options: not limited (like `aws:PrincipalArn` with `"*"`) or limited (like a role ARN) | S-PRINCIPAL |
| The account (`<acct>` or `arn:aws:iam::<acct>:root`) | any principal of the account | identity policy, boundary and session policy (flow chart "Allows the account of the calling principal" goes to the identity check) | role and user flow charts |

AWS's own summary for boundaries: "If you specify a role session or user in the principal element of a resource-based policy, an explicit allow in the permission boundary is not required. However, if you specify a role ARN in the principal element of a resource-based policy, an explicit allow in the permission boundary is required. In both cases, an explicit deny in the permission boundary is effective" (S-POLICIES `#policies_bound`). For session policies: a resource policy naming the user or role ARN is limited by the session policy ("The session policy limits the total permissions granted by the resource-based policy and the identity-based policy"); one naming the session ARN is not ("The resource-based policy permissions are not limited by the session policy") (S-POLICIES `#policies_session`).

### 2.4 Cross-account decision procedure

- "AWS evaluates the request in the trusting account and the trusted account ... The request is allowed only if both evaluations return a decision of Allow" (S-XACCT `#policy-eval-cross-account`).
- Trusted account (the principal's), from the detailed flow chart: explicit deny check; SCP; "Does the principal have an identity-based policy?" else implicit deny; "Is there an Allow in the identity-based policy?" else implicit deny; for a user, the boundary if any; for a role session, the boundary if any, then the session policy. There is no shortcut: the identity policy must allow whatever form the resource policy names.
- Trusting account (the resource's): explicit deny check; RCP; "Is there an Allow in the resource-based policy? If so, which principal?" else implicit deny; the matched form (session, role or account) must match the caller.
- Either side's explicit deny makes the final decision explicit deny (S-XACCT example: "Because the request is explicitly denied within one of the accounts, the final decision is to deny the request").
- "IAM roles and resource-based policies delegate access across accounts only within a single partition" (S-XRES Note). understudy has one partition, `aws`.
- The trusting account can delegate no more than it was granted; "If you try to delegate more permissions than your account has, your principals will still have only list access" (S-XRES `#access_policies-cross-account-delegating-resource-based-policies`).

### 2.5 Organizations SCPs and RCPs

- "If there is no SCP, or if the SCP allows the requested action, the enforcement code evaluation continues"; same for RCPs (S-ENF). understudy emulates no AWS Organizations, so every account is outside an organization: the SCP and RCP steps always continue, and the organization keys (`aws:PrincipalOrgID`, `aws:PrincipalOrgPaths`, `aws:ResourceOrgID`, `aws:ResourceOrgPaths`, `aws:SourceOrgID`, `aws:SourceOrgPaths`) are never in the context (S-KEYS: "included ... only if the principal is a member of an organization").
- `RCPFullAWSAccess` exists only when RCPs are enabled (S-ENF); nothing to emulate.
- Recording accounts on the free plan do carry AWS-managed SCPs (`docs/recording-kit.md` 1); a capture that answers "explicit deny in a service control policy" is the guardrail, not the behavior under test.

### 2.6 The account root user

- "By default, all requests are implicitly denied with the exception of the AWS account root user, which has full access" (S-ENF). The root user cannot have identity policies or a permissions boundary, can be named in a resource policy, and is bound by its organization's SCPs and RCPs (S-POLICIES `#access_policies-root`). understudy models root's full access as an identity-policy Allow of every action on every resource of its own account (inferred).
- An explicit deny still applies to root: S3-PBP's exception below holds "even if their bucket policy explicitly denies the root principal's access", which presumes the deny otherwise binds root, and K-POL says root has no key permission unless "never denied" (inferred from both). The root user page lists "Remove a misconfigured bucket policy that denies all principals" and "Delete an Amazon Simple Queue Service resource-based policy that denies all principals" among the privileged tasks for member accounts, and, under "Amazon Simple Queue Service Task", "Edit or delete an Amazon SQS resource-based policy that denies all principals" as a task only the root user can perform (S-ROOT `#root-user-tasks`).
- S3 exception, documented: "the root principal in a bucket owner's AWS account can perform the GetBucketPolicy, PutBucketPolicy, and DeleteBucketPolicy API actions, even if their bucket policy explicitly denies the root principal's access. Bucket owner root principals can only be blocked from performing these API actions by VPC endpoint policies and AWS Organizations policies" (S3-PBP). Also: "If you are using an identity other than the root user of the AWS account that owns the bucket, the calling identity must both have the PutBucketPolicy permissions on the specified bucket and belong to the bucket owner's account" (same page). Whether SQS lets the queue owner's root remove a deny-all queue policy the same way through the API is UNVERIFIED (IAM-EVAL-23); options: a bypass for `sqs:SetQueueAttributes` and `sqs:DeleteQueue` like S3's, or no bypass. S-ROOT's SQS root-user task (above) points to a bypass for editing or deleting the queue policy, but names no API action.
- KMS: "No AWS principal, including the account root user or key creator, has any permissions to a KMS key unless they are explicitly allowed, and never denied, in a key policy, IAM policy, or grant" (K-POL). Root reaches a key through the account-principal statement (4.3).
- Root cannot assume roles: STS lists who can call `AssumeRole` as "IAM user or IAM role with existing temporary security credentials", against "IAM user or AWS account root user" for `GetFederationToken` and `GetSessionToken` (S-STSCMP). The wire answer is UNVERIFIED (IAM-EVAL-22).
- In an `aws:PrincipalArn` condition, `arn:aws:iam::<acct>:root` "limits permissions only for the root user"; in a `Principal` element the same ARN "delegates authority to the account" (S-KEYS `#condition-keys-principalarn`, S-PRINCIPAL `#principal-accounts`).

### 2.7 Session principals and session policies

- Session principals are role sessions and STS federated-user sessions (S-ENF). A role session's permissions are "the intersection of the role's identity-based policy and the session policies"; session policies "cannot be used to grant more permissions" (STS-AR; S-ARPERM). The caller's own policies do not carry into the session: "The user temporarily gives up its original permissions in favor of the permissions assigned by the assumed role" (S-ARPERM).
- `AssumeRole` takes one inline `Policy` and up to 10 managed `PolicyArns`; "The plaintext that you use for both inline and managed session policies can't exceed 2,048 characters"; the managed ARNs "must exist in the same account as the role" (STS-AR). Incomplete ARNs (fewer than six fields) are not completed in session policies: "Using an incomplete ARN in a session policy will result in a MalformedPolicyDocumentException error" (S-GRAMMAR `#policies-grammar-notes-strings`).
- Several session policies of one session are policies of the same type, so they combine with OR before the intersection (S-POLICIES `#policies-introduction`; inferred application).
- Credential restrictions independent of policies (S-STSCMP): `AssumeRole` credentials "Cannot call GetFederationToken or GetSessionToken"; `GetSessionToken` credentials "Cannot call IAM API operations unless MFA information is included with the request" and "Cannot call AWS STS API operations except AssumeRole or GetCallerIdentity"; `GetFederationToken` credentials cannot call IAM operations or STS operations except `GetCallerIdentity`.
- Session tags are the session's principal tags and "override existing assumed role or federated user session tags with the same tag key, regardless of character case"; STS "does not support multi-valued session tags"; passing tags needs `sts:TagSession` in the trust policy: "For roles without this permission in the trust policy, the AssumeRole operation fails" (S-TAGS `#id_session-tags_know`).
- Role chaining: by default tags do not pass to the next session; transitive tags do; a chained session is capped at one hour (S-TAGS; STS-AR `DurationSeconds`).
- Federated-user sessions (`GetFederationToken`) are out of scope: the operation is unbound in understudy (`docs/coverage.md` sts).

### 2.8 Permissions boundaries

- A boundary is a managed policy (AWS or customer) on a user or role that "sets the maximum permissions that an identity-based policy can grant"; it "does not provide permissions on its own" (S-BOUND).
- Identity and boundary: the effective permissions are the intersection; an explicit deny in either wins (S-BOUND `#access_policies_boundaries-eval-logic`). Boundary, identity and session policy: intersection of all three (same section).
- Resource policies: not limited by a boundary when they name the user or the session; limited when they name the role (2.3). "Resource-based policies are not limited by permissions boundaries" is the troubleshooting page's summary for the user case (S-AD).
- NotPrincipal: "The NotPrincipal element with a Deny effect will always deny any IAM principal that has a permissions boundary policy attached, regardless of the values specified in the NotPrincipal element" (S-BOUND, Important; S-NOTPRIN `#notprincipal-key-points`).
- The context key `iam:PermissionsBoundary` (used in vectors F3 and F4) is the boundary ARN a request passes (S-BOUND `#access_policies_boundaries-delegate`).

### 2.9 Service principals, aws:SourceArn and aws:SourceAccount

- A service principal is `<service>.amazonaws.com`; `"Service": "*"` is not allowed; several services go in one `Service` array (S-PRINCIPAL `#principal-services`). A service in an opt-in Region calling another Region uses the regional name `<service>.<region>.amazonaws.com` (S-PRINCIPAL `#principal-services-in-opt-in-regions`). understudy models no opt-in Regions, so its service principals are always the non-regional names (inferred).
- A service principal has no identity policies; only a resource policy can allow it (flow charts; inferred).
- `aws:SourceArn` and `aws:SourceAccount` are present "only when the call to your resource is being made directly by an AWS service principal on behalf of a resource for which the configuration triggered the service-to-service request"; `aws:SourceArn` "does not work with the ARN of the principal making the request" (S-KEYS `#condition-keys-sourcearn`, `#condition-keys-sourceaccount`). `aws:PrincipalIsAWSService` is true for such calls and false when a service uses an IAM principal's credentials or a service role (S-KEYS `#condition-keys-principalisawsservice`). `aws:PrincipalServiceName` is present only for service-principal calls (S-KEYS `#condition-keys-principalservicename`).
- AWS recommends `aws:SourceArn`, `aws:SourceAccount`, `aws:SourceOrgID` or `aws:SourceOrgPaths` on every grant to a service principal (S-DEPUTY `#cross-service-confused-deputy-prevention`).
- understudy's deliveries already carry a service principal and a source (`internal/access/doc.go`, `Advise`; ARCHITECTURE.md 16). Whether a service-principal request carries `aws:PrincipalType`, `aws:userid` and `aws:PrincipalAccount` is UNVERIFIED (IAM-EVAL-33): the principal key table (S-VARS `#principaltable`) has no service-principal row; options are absent or a service-specific value. Whether `"Principal": "*"` (or `{"AWS": "*"}`) admits a service principal is UNVERIFIED (IAM-EVAL-33); policydoc currently says yes (`internal/policydoc/doc.go`).

### 2.10 Anonymous callers

- Only a resource policy with `"Principal": "*"` or `{"AWS": "*"}` grants an anonymous caller; the two forms are equivalent for anonymous users (S-PRINCIPAL `#principal-anonymous`).
- The principal key table lists anonymous callers for "Amazon SQS, Amazon SNS, and Amazon S3 only", with `aws:userid` = `anonymous`, `aws:PrincipalType` = `Anonymous` and no `aws:username` (S-VARS `#principaltable`); `aws:PrincipalAccount` is `anonymous` (S-KEYS `#condition-keys-principalaccount`); `aws:PrincipalArn` and `aws:PrincipalIsAWSService` are absent (S-KEYS).

### 2.11 Explicit and implicit denies on the wire

- An explicit deny is an applicable `Deny` statement; an implicit deny is the absence of any applicable Allow and Deny (S-DENY).
- Message grammar (S-AD `#access-denied-error-examples`): "Most access denied error messages appear in the format `User <user> is not authorized to perform <action> on <resource> because <context>`". Implicit: `because no <type> policy allows the <action> action`. Explicit: `with an explicit deny in a <type> policy`, optionally `: <policy ARN>`. The `on resource:` part is omitted in several of AWS's own examples. Only one policy type is named when several deny (S-AD). Recorded `<type>` spellings: `service control policy`, `resource control policy`, `VPC endpoint policy`, `permissions boundary` (no "policy": `because no permissions boundary allows the codedeploy:ListDeployments action`, `with an explicit deny in a permissions boundary`), `session policy`, `resource-based policy`, `role trust policy` (explicit form: `with an explicit deny in the role trust policy`), `identity-based policy`. Examples, verbatim:

```
User: arn:aws:iam::123456789012:role/HR is not authorized to perform: codecommit:ListRepositories
because no identity-based policy allows the codecommit:ListRepositories action

User: arn:aws:iam::123456789012:role/HR is not authorized to perform: codedeploy:ListDeployments
on resource: arn:aws:codedeploy:us-east-1:123456789012:deploymentgroup:*
with an explicit deny in an identity-based policy: arn:aws:iam::123456789012:policy/HRAccessPolicy

User: arn:aws:iam::123456789012:user/John is not authorized to perform: sts:AssumeRole
because no role trust policy allows the sts:AssumeRole action
```

  The examples break the message across lines for display; the wire message is one line (inferred). "Some AWS services do not support this access denied error message format" (S-AD).
- Wire codes per understudy service, from the pinned models (`smithy.api#error` shapes whose names contain AccessDenied or Authorization):

| Service | Code | Status | Source |
|---|---|---|---|
| S3 | `AccessDenied` | 403 | `com.amazonaws.s3#AccessDenied` (`httpError` 403) |
| SNS | `AuthorizationError` | 403 | `com.amazonaws.sns#AuthorizationErrorException` (`awsQueryError` code `AuthorizationError`, 403) |
| CloudWatch Logs | `AccessDeniedException` | 400 | `com.amazonaws.cloudwatchlogs#AccessDeniedException` (client, no `httpError`, so 400; wire-json-query.md 9.2); third-party-recorded 400 at localstack@8b9a79f `tests/aws/services/logs/test_logs_subscription_filters.snapshot.json:L157-L166` (validated 2026-02-06, a service-generated denial) |
| EventBridge | `AccessDeniedException` | 400 | `com.amazonaws.eventbridge#AccessDeniedException` (client, no `httpError`) |
| Lambda | `AccessDeniedException` | 403 | not modeled; third-party-recorded at localstack@8b9a79f `tests/aws/services/lambda_/test_lambda_api.snapshot.json:L13926-L13935` (validated 2025-11-25, `Message` null, a cross-account function ARN; lambda-api.md 2.1) |
| Secrets Manager | `AccessDeniedException` | 400 | not modeled; third-party-recorded at localstack@8b9a79f `tests/aws/services/secretsmanager/test_secretsmanager.snapshot.json:L4011-L4020` (validated 2024-03-15, a service-generated denial) |
| SQS, DynamoDB, KMS, IAM, STS, CloudWatch | not modeled | | UNVERIFIED for policy denials (IAM-EVAL-21); IAM's common errors list `AccessDenied`/`AccessDeniedException` 403 (iam-sts-kms.md 1.2) |

  For the Lambda and Secrets Manager rows and every policy-denial message text, the exact text for an IAM policy denial is UNVERIFIED (IAM-EVAL-21).
- `sts:GetCallerIdentity` needs no permission and works under an explicit deny (iam-sts-kms.md 4.1).

## 3. Role trust policies and AssumeRole

### 3.1 A trust policy must allow explicitly

- "In IAM roles, use the Principal element in the role trust policy to specify who can assume the role" (S-PRINCIPAL). The trust policy is the role's resource policy and is one of the two exceptions that "must explicitly allow access for principals" (S-ENF "Resource-based policies"). The single-account flow chart ends in implicit deny when the resource is an IAM role and its resource policy does not allow.
- Actions evaluated: `sts:AssumeRole` for every call; `sts:TagSession` too when the call passes session tags (S-TAGS `#id_session-tags_permissions-required`). The trust policy may test `sts:ExternalId`, `sts:SourceIdentity`, `sts:TransitiveTagKeys`, `aws:RequestTag/*`, `aws:TagKeys` and `aws:MultiFactorAuthPresent` (STS-AR; S-TAGS; S-KEYS).

### 3.2 Same account: principal ARN versus account

"To allow a user to assume a role in the same account, you can do either of the following: Attach a policy to the user that allows the user to call AssumeRole (as long as the role's trust policy trusts the account). Add the user as a principal directly in the role's trust policy. You can do either because the role's trust policy acts as an IAM resource-based policy. When a resource-based policy grants access to a principal in the same account, no additional identity-based policy is required" (STS-AR). So the 2.3 table applies with the role as the resource: a trust naming the user or session needs nothing else; a trust naming the account needs the caller's identity policy to allow `sts:AssumeRole` on the role ARN (S-SWITCH `#roles-usingrole-createpolicy`).

### 3.3 Cross account

"To assume a role from a different account, your AWS account must be trusted by the role ... A user who wants to access a role in a different account must also have permissions that are delegated from the account administrator. The administrator must attach a policy that allows the user to call AssumeRole for the ARN of the role in the other account" (STS-AR). Both evaluations of 2.4 apply.

### 3.4 Roles and role sessions as principals

- A session's ARN is `arn:aws:sts::<acct>:assumed-role/<role-name>/<role-session-name>` (S-IDS `#identifiers-arns`); it shows no role path (iam-sts-kms.md 4.2). The principal making a request with role credentials "is the IAM role session ARN and not the ARN of the role itself" (S-ENF).
- `aws:PrincipalArn` for a session is the role's ARN: "For IAM roles, the request context returns the ARN of the role, not the ARN of the user that assumed the role ... Do not specify the assumed role session ARN as a value for this condition key" (S-KEYS `#condition-keys-principalarn`). Whether that ARN includes the role's path is UNVERIFIED (IAM-EVAL-20); options: `role/<path><name>` (the role's own ARN) or `role/<name>`.
- AWS binds trust-policy principals to unique IDs: "If your Principal element in a role trust policy contains an ARN that points to a specific IAM role, then that ARN transforms to the role's unique principal ID when you save the policy ... if you delete the role, then you break the relationship. The policy no longer applies, even if you recreate the role ... the principal ID appears in resource-based policies because AWS can no longer map it back to a valid ARN" (S-PRINCIPAL `#principal-roles`; same for users, `#principal-users`). Resource policies may also name unique IDs directly (`AIDA...`, `AROA...`; S-IDS `#identifiers-unique-ids`). Condition-key ARNs are not converted: "Because AWS does not convert condition key ARNs to IDs, permissions granted to the role ARN persist if you delete the role and then create a new role with the same name" (S-PRINCIPAL `#principal-roles`).
- Save-time validation of named principals: Terraform retries `CreateRole` and `UpdateAssumeRolePolicy` for 2 minutes on `MalformedPolicyDocumentException` containing `Invalid principal in policy` (hashicorp/terraform-provider-aws v6.68.0 `internal/service/iam/role.go:L368`, `:L602`; `internal/service/iam/consts.go:L16`), KMS `CreateKey` on any `MalformedPolicyDocumentException` for 2 minutes (`internal/service/kms/wait.go:L24`, `:L28-L29`; `internal/service/kms/key.go:L50`, `:L179`) and the key-policy update on `NotFoundException` or `MalformedPolicyDocumentException` for 3 minutes (`internal/service/kms/key.go:L554`; `internal/service/kms/wait.go:L19`), Secrets Manager `PutResourcePolicy` on `MalformedPolicyDocumentException` containing `This resource policy contains an unsupported principal` for 2 minutes (`internal/service/secretsmanager/secret.go:L444-L446`; `internal/service/secretsmanager/consts.go:L11`), S3 `PutBucketPolicy` on `MalformedPolicy` (and `NoSuchBucket`) for 2 minutes (`internal/service/s3/bucket_policy.go:L69-L71`; `internal/service/s3/bucket.go:L52`). The KMS model states it: "The principals in the key policy must exist and be visible to KMS" (`com.amazonaws.kms#PutKeyPolicyRequest$Policy`). The IAM text and the display of a deleted principal's ID are UNVERIFIED (IAM-EVAL-24).

### 3.5 Who may call AssumeRole

- Callers are IAM users and role sessions, not the root user (S-STSCMP); the root case is UNVERIFIED on the wire (IAM-EVAL-22). understudy's current deviation, "the account root user may assume roles, which AWS refuses, because unminted keys resolve to root (ADR 0012)", is recorded in `docs/coverage.md` (sts AssumeRole).
- MFA: a trust policy condition such as `{"Bool": {"aws:MultiFactorAuthPresent": true}}` denies a caller who did not pass valid `SerialNumber` and `TokenCode`; "If the role being assumed requires MFA and if the TokenCode value is missing or expired, the AssumeRole call returns an 'access denied' error" (STS-AR).
- Duration, session name, external id and source identity constraints are in iam-sts-kms.md 4.2.

### 3.6 Service roles and Lambda execution roles

- A service role's trust policy names the service principal (S-PRINCIPAL `#principal-services`). understudy's Lambda execution environments obtain role sessions on the STS path (ARCHITECTURE.md 7); under enforcement the role's trust policy must allow `lambda.amazonaws.com` (inferred from the same section of S-PRINCIPAL). AWS answers `CreateFunction` with an untrusting role with `InvalidParameterValueException` containing `The role defined for the function cannot be assumed by Lambda`, which Terraform retries for 5 minutes (tf-compute.md 2.1, tf-compute.md 8); an event source mapping whose role lacks SQS permissions fails at creation (lambda-events.md 1.3).

## 4. KMS key policies

### 4.1 The key policy is required

- "Every KMS key must have exactly one key policy ... Unless the key policy explicitly allows it, you cannot use IAM policies to allow access to a KMS key. Without permission from the key policy, IAM policies that allow permissions have no effect. (You can use an IAM policy to deny a permission to a KMS key without permission from a key policy.)" (K-POL). Key policies are Regional (K-POL).
- "You can use the key policy alone to control access if the key and the IAM principal are in the same AWS account ... when a caller in one account must access a key in a different account, you cannot use key policy alone to grant access" (K-CTL `#intro-key-policy`).

### 4.2 The KMS authorization flow

The flow chart on K-EVAL (read as an image), in order:

1. An applicable Deny statement: deny.
2. An SCP that does not allow: deny. A VPC endpoint policy that does not allow: deny. (Neither exists in understudy.)
3. Does the key policy allow the caller to perform the action? If yes, go to 6.
4. Does a grant allow the caller? If yes, go to 6. (Grants are out of scope, 4.6.)
5. Does the key policy allow the caller's account to use IAM policies? (or a grant allow that account?) If neither, deny. Then: does an IAM policy allow the caller to perform this action? If no, deny; if yes, go to 6.
6. Is the key in the same account as the caller? If yes, allow. If no: a grant created by the caller's account allowing the caller, or an IAM policy in the caller's account allowing the caller: allow; else deny.

Steps 5 and 6 make a cross-account caller need its own IAM policy even when the key policy names it directly (consistent with 2.4).

### 4.3 The "Enable IAM User Permissions" statement

- The default key policy for keys created through the API is exactly one statement, Sid `Enable IAM User Permissions`, principal `arn:aws:iam::<acct>:root`, `kms:*` on `*` (K-DEF `#key-policy-default-allow-root-enable-iam`; iam-sts-kms.md 5.3).
- "When the principal in a key policy statement is the account principal, the policy statement doesn't give any IAM principal permission to use the KMS key. Instead, it allows the account to use IAM policies to delegate the permissions specified in the policy statement" (K-DEF). "A principal in arn:aws:iam::111122223333:root format does not represent the AWS account root user ... However, the account principal represents the account and its administrators, including the account root user" (K-OVR `#key-policy-elements`). Without the statement, "IAM policies that allow access to the key are ineffective, although IAM policies that deny access to the key are still effective" (K-DEF).
- A key policy naming a role ARN is a resource-policy grant to a role ARN; whether a permissions boundary limits it, as 2.3 says for resource policies in general, is UNVERIFIED for KMS (IAM-EVAL-27): the KMS flow chart shows no boundary step. Options: limited (the general rule) or not limited.

### 4.4 Cross-account key use

- "Cross-account access requires permission in the key policy of the KMS key and in an IAM policy in the external user's account ... Neither the key policy nor the IAM policy alone is sufficient" (K-XACCT).
- "Cross-account permission is effective only for the following operations: Cryptographic operations, CreateGrant, DescribeKey, GetKeyRotationStatus, GetPublicKey, ListGrants, RetireGrant, RevokeGrant. If you give a user in a different account permission for other operations, those permissions have no effect" (K-XACCT). understudy's switch `kms.cross-account.management-error` (value `NotFoundException`, inferred) already models the refusal; AWS's error is UNVERIFIED (IAM-EVAL-30).
- A key policy `"Principal": {"AWS": "*"}` statement lets any account's principal use the key "whenever they have corresponding permissions in their own account" (K-OVR `#key-policy-elements`).

### 4.5 Key policy document rules

From K-OVR `#key-policy-format` and `#key-policy-elements` and the model (`com.amazonaws.kms#PutKeyPolicyRequest`):

- Up to 32 kilobytes (32,768 bytes) (K-OVR `#key-policy-format`); printable ASCII, Basic Latin and Latin-1 Supplement characters, tab, line feed and carriage return; `Sid` may contain spaces (IAM `Sid` may not). The model's `com.amazonaws.kms#PolicyType` allows 1 to 131,072 characters, so the 32 KB limit is a service check beyond the shape (11, C11).
- `Effect`, `Principal`, `Action` and `Resource` are required, `Resource` is `"*"` and means "this KMS key". A statement missing `Action` or `Resource` "has no effect", yet `CreateKey` and `PutKeyPolicy` succeed with it.
- Lockout safety check: "The key policy must allow the calling principal to make a subsequent PutKeyPolicy request on the KMS key ... (To omit this condition, set BypassPolicyLockoutSafetyCheck to true.)" The exact rejection text is UNVERIFIED (IAM-EVAL-29).
- "Each statement in the key policy must contain one or more principals. The principals in the key policy must exist and be visible to KMS" (model). The rejection text is UNVERIFIED (IAM-EVAL-29).

### 4.6 Grants (out of scope)

Grants are a third KMS authorization mechanism (K-CTL `#intro-kms-grants`; K-EVAL flow chart steps 4 to 6). understudy leaves `CreateGrant`, `ListGrants`, `RetireGrant` and `RevokeGrant` unbound (`docs/coverage.md` kms) and the evaluator treats every grant question in 4.2 as "no". This is honest only while no grant can exist; binding any grant operation requires implementing the grant branches.

## 5. Statement matching: Principal, Action, Resource

### 5.1 When a statement applies and how statements combine

- A statement applies when its principal element matches the caller (resource and trust policies only), its action element matches the action, its resource element matches the resource, and its whole Condition block is true (S-CTX `#reqcontext_usage`; S-COND).
- Grammar: `Version` and `Id` optional, `Statement` one statement or a list; each statement has optional `Sid`, optional `Principal`/`NotPrincipal`, `Effect`, `Action`/`NotAction`, `Resource`/`NotResource`, optional `Condition`; elements in any order; no element twice; a single value may drop its brackets (S-GRAMMAR `#policies-grammar-bnf`, `#policies-grammar-notes`). Validation texts are policydoc's (iam-sts-kms.md 3.3).
- `Effect` is `Allow` or `Deny` (S-GRAMMAR).

### 5.2 Action and NotAction

- `<service-prefix>:<action>`; "The prefix and the action name are case insensitive. For example, iam:ListAccessKeys is the same as IAM:listaccesskeys" (S-ACTION). `*` and `?` may appear anywhere in the action name: `iam:*AccessKey*` covers `CreateAccessKey`, `DeleteAccessKey`, `ListAccessKeys`, `UpdateAccessKey` (S-ACTION). `"Action": "*"` is every action (S-GRAMMAR `#policies-grammar-notes-strings`).
- `NotAction` "matches everything except the specified list of actions"; with Allow it grants every action not listed that applies to the statement's resources; with Deny it denies every action not listed (S-NOTACTION). `"NotAction": "s3:DeleteBucket"` with `"Resource": "arn:aws:s3:::*"` "does not allow actions in other services, because other service actions are not applicable to the S3 resources" (S-NOTACTION), that is, the resource element still has to match.

### 5.3 Resource and NotResource

- An ARN; `*` and `?` within segments; "If the * wildcard is the last character of a resource ARN segment, it can expand to match beyond the colon boundaries"; `*` "can expand to replace everything within a segment, including characters like a forward slash (/)"; `/` "is treated the same as any other valid character"; no wildcard in the service segment (S-RESOURCE `#reference_policies_elements_resource_wildcards`), nor in the partition, nor in the resource-type portion (`arn:aws:lambda:us-east-2:123456789012:functi*:my-function` "not allowed"; S-ARNS `#arns-paths-wildcards`).
- Incomplete ARNs: "When you specify an incomplete ARN (one with fewer than the standard six fields) in an identity-based policy, AWS automatically completes the ARN by adding wildcard characters (*) to all missing fields. For example, specifying arn:aws:sqs is equivalent to arn:aws:sqs:*:*:*"; session policies reject them (S-GRAMMAR `#policies-grammar-notes-strings`; S-ARNS). Whether resource-based policies and ARN condition values get the same completion is UNVERIFIED (IAM-EVAL-11).
- Case: "In the Resource element, the IAM user name is case sensitive" (S-RESOURCE); ARN condition operators are "Case-sensitive matching of the ARN" (S-OPS `#Conditions_ARN`). Case sensitivity of other ARN parts in `Resource` is UNVERIFIED (IAM-EVAL-10); understudy matches whole ARNs case-sensitively (inferred from the two statements).
- `NotResource` "explicitly matches every resource except those specified" (S-NOTRES); with Deny it denies the statement's actions on every unlisted resource, with Allow it grants them (S-NOTRES `#notresource-element-combinations`).
- KMS key policies use `"Resource": "*"` to mean the key (4.5); trust policies have no Resource element and apply to the role (inferred from S-GRAMMAR's note that `principal_block` is required in trust policies and from policydoc's ValidateTrust).

### 5.4 Principal and NotPrincipal

- `Principal` is `"*"` or a map of `AWS`, `Federated`, `Service`, `CanonicalUser` to one value or a list (S-GRAMMAR). Values in an array are OR: "you authenticate as one principal at a time" (S-PRINCIPAL `#Principal_specifying`). "You cannot use a wildcard to match part of a principal name or ARN" (S-PRINCIPAL). Groups are never principals (S-PRINCIPAL).
- `AWS` values: an account id or `arn:aws:iam::<acct>:root` (equivalent, delegating to the account), a user ARN (user name case-sensitive), a role ARN, a role session ARN, a federated-user ARN (S-PRINCIPAL), or a unique id (S-IDS `#identifiers-unique-ids`). "When you save a resource-based policy that includes the shortened account ID, the service might convert it to the principal ARN" (S-PRINCIPAL `#principal-accounts`); which of understudy's services do is UNVERIFIED (IAM-EVAL-25). Terraform's policy comparison treats a 12-digit id and `arn:<partition>:iam::<id>:root` as equal (hashicorp/awspolicyequivalence v1.8.0 `aws_policy_equivalence.go:L316-L335`), so either answer leaves the second plan empty.
- `Federated` (OIDC and SAML) principals are valid only in trust policies (S-PRINCIPAL `#principal-federated-web-identity`) and are out of scope (`AssumeRoleWithWebIdentity` and `AssumeRoleWithSAML` are not evaluated). `CanonicalUser` is S3-only (S-GRAMMAR `#policies-grammar-notes`).
- `NotPrincipal` must be used with Deny; it is not allowed in identity policies, trust policies, SCPs or RCPs (S-NOTPRIN `#notprincipal-key-points`). "When you use NotPrincipal, you must also specify the account ARN of the not-denied principal. Otherwise, the policy might deny access to the entire account containing the principal ... If an assumed-role user ... is being evaluated, AWS might validate the account first, then the role, and then the assumed-role user ... we strongly recommend that you explicitly include the ARN for a user's account, or include both the ARN for a role and the ARN for the account containing that role" (S-NOTPRIN). Which listed identities exempt a role session is UNVERIFIED (IAM-EVAL-18); options: exempt when any listed value names the caller, its role or its account; exempt only when the account and the role (or session) are both listed; exempt only when account, role and session ARN are all listed.

### 5.5 Version 2008-10-17 versus 2012-10-17

- Supported values are `2012-10-17` and `2008-10-17` (S-VERSION; S-GRAMMAR). In `2008-10-17`, "variables such as ${aws:username} aren't recognized as variables and are instead treated as literal strings" (S-VERSION). "If you don't include the Version element and set it to an appropriate version date, variables like ${aws:username} are treated as literal strings" (S-VARS `#policy-vars-using-variables`). The special-character variables `${*}`, `${?}`, `${$}` are policy variables (S-VARS `#policy-vars-specialchars`), so they too are literal outside `2012-10-17` (inferred; policydoc already does this, `internal/policydoc/doc.go`).
- No other evaluation difference between the versions is documented.

## 6. Condition evaluation

### 6.1 Block logic

S-MULTI `#reference_policies_multiple-conditions-eval`:

- Several operators in one Condition block: AND.
- Several keys under one operator: AND.
- Several values for one key under a positive operator: OR.
- Several values for one key under a negated operator: NOR ("returns true only if all values evaluate to false", `#reference_policies_multiple-conditions-negated-matching-eval`).
- "All context keys in a condition element block must resolve to true to invoke the desired Allow or Deny effect."

### 6.2 Key names, value case and the absent-key rule

- "Context key names are not case-sensitive. For example, including the aws:SourceIP context key is equivalent to testing for AWS:SourceIp. Case-sensitivity of context key values depends on the condition operator" (S-COND). Policy variable names likewise: "aws:CurrentTime is equivalent to AWS:currenttime" (S-VARS).
- The absent-key rule: "If the key that you specify in a policy condition is not present in the request context, the values do not match and the condition is false. If the policy condition requires that the key is not matched, such as StringNotLike or ArnNotLike, and the right key is not present, the condition is true. This logic applies to all condition operators except ...IfExists and Null check" (S-OPS, Important). S-COND: "A context key that is not present in the request is considered a mismatch."
- Policy values may be strings, numbers or booleans; "Quotation marks are optional for numeric and Boolean values" (S-GRAMMAR `#policies-grammar-json`; S-TYPES).

### 6.3 String operators

S-OPS `#Conditions_String`; variables and wildcards supported.

| Operator | Test per policy value |
|---|---|
| `StringEquals` | exact, case-sensitive |
| `StringNotEquals` | negated exact |
| `StringEqualsIgnoreCase` | exact, ignoring case |
| `StringNotEqualsIgnoreCase` | negated, ignoring case |
| `StringLike` | case-sensitive with `*` (any run) and `?` (one character) anywhere; "You must specify wildcards to achieve partial string matches" |
| `StringNotLike` | negated `StringLike` |

Which case folding `IgnoreCase` uses for non-ASCII text is UNVERIFIED (IAM-EVAL-08); policydoc uses Go's `strings.EqualFold` (Unicode simple folding).

### 6.4 Numeric operators

`NumericEquals`, `NumericNotEquals`, `NumericLessThan`, `NumericLessThanEquals`, `NumericGreaterThan`, `NumericGreaterThanEquals` compare "to an integer or decimal value"; no variables, no wildcards (S-OPS `#Conditions_Numeric`). Accepted spellings (`10.0`, `+10`, `1e2`, `010`) are UNVERIFIED (IAM-EVAL-09).

### 6.5 Date operators

`DateEquals`, `DateNotEquals`, `DateLessThan`, `DateLessThanEquals`, `DateGreaterThan`, `DateGreaterThanEquals`, used with `aws:CurrentTime`, `aws:EpochTime` and other date keys; "You must specify date/time values with one of the W3C implementations of the ISO 8601 date formats or in epoch (UNIX) time"; no variables, no wildcards (S-OPS `#Conditions_Date`). `aws:EpochTime` is both Date and Numeric and "accepts the number of seconds since January 1, 1970" (S-KEYS `#condition-keys-epochtime`). Which W3C profiles (date only, fractional seconds, offsets) and whether epoch milliseconds parse is UNVERIFIED (IAM-EVAL-06).

### 6.6 Bool

`Bool` compares to `true` or `false`; `ForAllValues:Bool` and `ForAnyValue:Bool` apply to arrays of booleans (S-OPS `#Conditions_Boolean`). S-OPS lists policy variables as supported for Bool, S-VARS `#policy-vars-conditionelement` says variables cannot be used with Boolean operators: UNVERIFIED (IAM-EVAL-01). Spellings other than `true`/`false` (`True`, `TRUE`, `1`) are UNVERIFIED (IAM-EVAL-08).

### 6.7 BinaryEquals

"compares the value of the specified key byte for byte against a base-64 encoded representation of the binary value in the policy"; absent key, no match; no variables or wildcards (S-OPS `#Conditions_BinaryEquals`). No understudy operation is known to supply a binary key; the operator is implemented for completeness (inferred).

### 6.8 IpAddress and NotIpAddress

CIDR values, IPv4 or IPv6; "If you specify an IP address without the associated routing prefix, IAM uses the default prefix value of /32" (S-OPS `#Conditions_IPAddress`). `aws:SourceIp` is absent for requests through a VPC endpoint and, for a service calling on a user's behalf, carries the service's address (S-OPS; S-KEYS `#condition-keys-sourceip`). The default prefix for an IPv6 value is UNVERIFIED (IAM-EVAL-07); options: `/128` (a single address) or `/32` as written.

### 6.9 ARN operators

`ArnEquals` and `ArnLike` behave identically: "Case-sensitive matching of the ARN. Each of the six colon-delimited components of the ARN is checked separately and each can include multi-character match wildcards (*) or single-character match wildcards (?)"; `ArnNotEquals` and `ArnNotLike` are the negations, also identical; variables and wildcards supported; "The ARN is considered a string" (S-OPS `#Conditions_ARN`). A six-component split keeps colons inside the resource component (policydoc `arnMatches`). The result for an incomplete pattern or a context value that is not an ARN is UNVERIFIED (IAM-EVAL-11).

### 6.10 ...IfExists

"You can add IfExists to the end of any condition operator name except the Null condition ... If the condition key is present in the context of the request, process the key as specified in the policy. If the key is not present, evaluate the condition element as true ... If you are using an Effect: Deny element with a negated condition operator like StringNotEqualsIfExists, the request is still denied even if the condition key is not present" (S-OPS `#Conditions_IfExists`).

### 6.11 Null

`"Null": {"<key>": "true"}` holds when the key is absent ("it is null"); `"false"` holds when "the key exists and its value is not null"; no policy variables (S-OPS `#Conditions_Null`). S-COND and S-OPS recommend `Null` false "to prevent missing context keys or context keys with empty values from evaluating to true", which implies an empty value counts as null; whether a present empty string satisfies `Null` false is UNVERIFIED (IAM-EVAL-05).

### 6.12 Multivalued keys and set operators

- Single-valued versus multivalued is a property of the request context, not the policy (S-SETS). The key reference marks each key's value type (S-KEYS); the Service Authorization Reference uses `ArrayOf...` types (S-SETS `#reference_policies_condition-key-points`). `aws:TagKeys`, `aws:PrincipalOrgPaths`, `aws:ResourceOrgPaths`, `aws:CalledVia`, `aws:PrincipalServiceNamesList`, `aws:SourceOrgPaths`, `aws:VpceOrgPaths` are multivalued; `aws:RequestTag/*`, `aws:ResourceTag/*`, `aws:PrincipalTag/*` are single-valued (S-SETS; S-KEYS).
- `ForAllValues`: "The condition returns true if every context key value in the request matches a context key value in the policy. It also returns true if there are no context keys in the request" (S-SETS `#reference_policies_condition-forallvalues`); also true "if the context key value resolves to a null dataset, such as an empty string" (S-OPS `#conditions_arn_multivalued`).
- `ForAnyValue`: "The condition returns true if any one of the context key values in the request matches any one of the context key values in the policy ... For no matching context key or if the key does not exist, the condition returns false" (S-SETS `#reference_policies_condition-foranyvalue`).
- Negated inner operators (S-OPS `#conditions_string_multivalued`, `#conditions_arn_multivalued`): `ForAllValues:StringNotEquals` "None of the values of the context key in the request can match any of the context key values in your policy"; `ForAnyValue:StringNotEquals` "At least one context key value in the request must NOT match any of values in the context key in your policy". The same shapes are documented for `StringNotLike`, `StringNotEqualsIgnoreCase`, `ArnNotEquals` and `ArnNotLike`.
- A multivalued string key with a wildcard or variable needs `StringLike` (S-SETS `#reference_policies_condition-multi-valued-context-keys`). Multivalued keys cannot be policy variables (S-SETS key points; S-VARS).
- "Do not use condition set operators ForAllValues or ForAnyValue with single-valued context keys. Using condition set operators with single-valued context keys can lead to overly permissive policies" (S-SETS key points). The result is UNVERIFIED (IAM-EVAL-03); the inferred model is a set of one value.
- A multivalued key under a plain operator: "Multivalued context keys require a condition set operator" (S-SETS), but the evaluation is UNVERIFIED (IAM-EVAL-02); options: true if any value matches, true only if all match, always false.
- ForAnyValue with a negated inner operator on an absent key, and either set operator on an empty value: UNVERIFIED beyond the documented `ForAllValues` empty case (IAM-EVAL-04); inferred: `ForAnyValue` false, as for any absent key.

### 6.13 Tag keys in key names

"Key names are not case-sensitive. This means that if you specify "aws:ResourceTag/TagKey1": "Value1" in the condition element of your policy, then the condition matches a resource tag key named either TagKey1 or tagkey1, but not both ... you might tag an Amazon EC2 instance with ec2=test1 and EC2=test2. When you use a condition such as "aws:ResourceTag/EC2": "test1" to allow access to that resource, the key name matches both tags, but only one value matches. This can result in unexpected condition failures" (S-COND). Which value AWS uses when two tag keys differ only by case is UNVERIFIED (IAM-EVAL-31); policydoc treats the key as absent (`internal/policydoc/allows.go` `contextValue`). The troubleshooting page warns the same for request key-value pairs: "If your request includes multiple key-value pairs with key names that differ only by case, then your access might be unexpectedly denied" (S-AD `#troubleshoot_general_access-denied-service`).

### 6.14 Truth table for implementation

`P` is the list of policy values for the key, `R` the request's values (one value for a single-valued key), `t(r, p)` the operator's test for one request value and one policy value (6.3 to 6.9). "Absent" means the key is not in the context.

| Operator | Key present | Key absent | Source |
|---|---|---|---|
| positive (`StringEquals`, `StringLike`, `StringEqualsIgnoreCase`, `Numeric*` except NotEquals, `Date*` except NotEquals, `Bool`, `BinaryEquals`, `IpAddress`, `ArnEquals`, `ArnLike`) | OR over P of `t(r, p)` | false | S-OPS, Important; 6.1 |
| negated (`StringNotEquals`, `StringNotEqualsIgnoreCase`, `StringNotLike`, `NumericNotEquals`, `DateNotEquals`, `NotIpAddress`, `ArnNotEquals`, `ArnNotLike`) | NOR over P of the positive test | true | S-OPS, Important; S-MULTI |
| `<op>IfExists` | as `<op>` | true | S-OPS `#Conditions_IfExists` |
| `Null` with `"true"` | false | true | S-OPS `#Conditions_Null` |
| `Null` with `"false"` | true (empty value: IAM-EVAL-05) | false | S-OPS `#Conditions_Null` |
| `ForAllValues:<op>` | every r in R satisfies `<op>` against P | true; also true for an empty value | S-SETS; S-OPS `#conditions_arn_multivalued` |
| `ForAnyValue:<op>` | some r in R satisfies `<op>` against P | false | S-SETS |
| plain `<op>` on a multivalued R | UNVERIFIED (IAM-EVAL-02) | as the row for `<op>` | S-SETS |

A set operator combined with `IfExists` (`ForAnyValue:StringLikeIfExists`) parses (policydoc `Operator`); absent then gives true by the IfExists rule (inferred).

## 7. Policy variables

### 7.1 Syntax and where variables may appear

- `${<key>}` with a single-valued key name (case-insensitive); "You can't use a multivalued condition key as a variable" (S-VARS `#policy-vars-using-variables`). Variables work in identity policies, resource policies, SCPs, session policies, VPC endpoint policies and boundaries (same section).
- Resource: "only in the resource portion of the ARN. This portion of the ARN appears after the fifth colon (:). You can't use a variable to replace parts of the ARN before the fifth colon" (S-VARS `#policy-vars-resourceelement`). Condition: string and ARN operators only, "You can't use a policy variable with other operators, such as Numeric, Date, Boolean, Binary, IP Address, or Null operators" (S-VARS `#policy-vars-conditionelement`; the Bool conflict is IAM-EVAL-01).
- What happens to a multivalued key used as a variable, to an unknown variable name, and to a variable in `Action` or `Principal` is UNVERIFIED (IAM-EVAL-14); options for each: the text stays literal, the variable resolves to no value, or the policy is rejected at save time.

### 7.2 Principal key values

S-VARS `#principaltable`:

| Principal | `aws:username` | `aws:userid` | `aws:PrincipalType` |
|---|---|---|---|
| Root user | not present | account id | `Account` |
| IAM user | the user name | the user's unique id | `User` |
| Federated user | not present | `<account>:<caller-specified-name>` | `FederatedUser` |
| OIDC or SAML federated | not present | `<role-id>:<caller-specified-role-name>` | `AssumedRole` |
| Assumed role | not present | `<role-id>:<RoleSessionName>` | `AssumedRole` |
| Role on an EC2 instance | not present | `<role-id>:<ec2-instance-id>` | `AssumedRole` |
| Anonymous (SQS, SNS, S3) | not present | `anonymous` | `Anonymous` |

"not present means that the value is not in the current request information, and any attempt to match it fails and causes the statement to be invalid" (S-VARS, same section; see 7.3).

### 7.3 Variables with no value

- "When policy variables reference a condition context key that has no value or is not present ... the value is effectively null. There is no equal or like value ... condition operators like StringEquals or StringLike do not match, and the policy statement does not take effect. Inverted condition operators like StringNotEquals or StringNotLike do match against a null value ... If a variable that has no value in the authorization context is used as part of the Resource or NotResource element of a policy, the resource that includes a policy variable with no value will not match any resource" (S-VARS `#policy-vars-no-value`).
- Two other sentences describe the missing value as invalidating the whole statement: "If AWS cannot resolve a variable this might cause the entire statement to be invalid" (S-VARS, Note at the top) and the principal table note above. Whether one unresolved value in a list voids the whole condition (or statement) or only that value is UNVERIFIED (IAM-EVAL-16). Whether a `NotResource` entry with a missing variable then excludes nothing, so the statement covers every resource, is UNVERIFIED (IAM-EVAL-13).

### 7.4 Defaults and special characters

- Default: "surround the default value with single quotes (' '), and separate the variable text and the default value with a comma and space (, )", for example `${aws:PrincipalTag/team, 'company-wide'}` (S-VARS `#policy-vars-default-values`). Tolerance for other spacing is UNVERIFIED (IAM-EVAL-15).
- `${*}`, `${?}`, `${$}` stand for a literal `*`, `?`, `$` "in any string where you can use regular policy variables" (S-VARS `#policy-vars-specialchars`). Their effect inside `StringEquals`, `Resource` and a `2008-10-17` document is UNVERIFIED in detail (IAM-EVAL-12); policydoc's reading is in `internal/policydoc/doc.go`.

## 8. Global condition keys

### 8.1 Availability and what understudy supplies

Availability, type and value type are from S-KEYS (each key's own anchor, `#condition-keys-<lowercase key>`). The understudy column is the decision for enforcement; "never" means the key is never in the context, which is also AWS's answer for a request outside an organization, a VPC or the named service.

| Key | Type | AWS includes it | understudy |
|---|---|---|---|
| `aws:PrincipalArn` | ARN, single | all signed requests; not anonymous | root, user or role ARN (`identity.Render`, except a session gives its role's ARN, path per IAM-EVAL-20) |
| `aws:PrincipalAccount` | String | all requests; `anonymous` for anonymous | principal's account; `anonymous` |
| `aws:PrincipalOrgPaths`, `aws:PrincipalOrgID` | String (list), String | organization members only | never |
| `aws:PrincipalTag/<k>` | String, single | user or role with tags, or session tags | user tags; role tags overridden by session tags, case-insensitively (S-TAGS) |
| `aws:PrincipalIsAWSService` | Bool | all signed requests | true for a service principal, else false; absent for anonymous |
| `aws:PrincipalServiceName` | String | service-principal calls only | the service principal's name |
| `aws:PrincipalServiceNamesList` | String list | service-principal calls only | one-element list of the name (inferred) |
| `aws:PrincipalType` | String | all requests | per 7.2; service principals per IAM-EVAL-33 |
| `aws:userid` | String | all requests | per 7.2 (`AIDA...`, `AROA...:<session>`, account id, `anonymous`) |
| `aws:username` | String | IAM users only | the user name |
| `aws:AssumedRoot`, `aws:FederatedProvider`, `aws:SignInSessionArn`, `aws:ChatbotSourceArn`, `aws:Ec2InstanceSourceVpc`, `aws:Ec2InstanceSourcePrivateIPv4`, `aws:ViaCustomerDomain`, `ec2:*`, `glue:*`, `codebuild:*`, `ssm:SourceInstanceArn`, `identitystore:UserId` | | only with the services or credential types named | never |
| `aws:TokenIssueTime` | Date | temporary credentials only | the session's issue instant on the emulator clock (inferred: compared with `aws:CurrentTime`, which is emulator time) |
| `aws:MultiFactorAuthAge` | Numeric | temporary credentials only; "not present for federated identities or requests made using access keys" | never (MFA is not checked, `docs/coverage.md` sts) |
| `aws:MultiFactorAuthPresent` | Bool | temporary credentials only; absent for long-term keys; a `Bool` false test "denies requests that can be authenticated using MFA, but were not. This applies only to temporary credentials that support using MFA" (`AssumeRole` does, S-STSCMP) | false in every session (switch `iam.context.mfa-present`; IAM-EVAL-26 confirms); absent for access keys |
| `aws:SourceIdentity` | String | after a source identity was set | the session's `SourceIdentity` when one was passed |
| `lambda:SourceFunctionArn` | ARN | "whenever Lambda function code is invoked" | the function ARN on calls signed with a Lambda execution environment's credentials (inferred) |
| `aws:SourceIp` | IP | except through a VPC endpoint | per switch `iam.context.source-ip` (8.2) |
| `aws:SourceVpc`, `aws:SourceVpcArn`, `aws:SourceVpce`, `aws:VpceAccount`, `aws:VpceOrgID`, `aws:VpceOrgPaths`, `aws:VpcSourceIp` | | through a VPC endpoint only | never |
| `aws:ResourceAccount` | String | most actions | the resource owner's account |
| `aws:ResourceOrgID`, `aws:ResourceOrgPaths` | | organization members only | never |
| `aws:ResourceTag/<k>` | String, single | resource has tags and the action supports tag authorization | the resource's tags for operations understudy declares ABAC-capable; other operations: unsupplied (8.2) |
| `aws:CalledVia`, `aws:CalledViaFirst`, `aws:CalledViaLast` | String list, String | a supporting service forwards the principal's credentials (FAS) | UNVERIFIED for S3 and Secrets Manager calls to KMS (IAM-EVAL-34); omitted until recorded |
| `aws:ViaAWSService` | Bool | always; true when a service uses forward access sessions for the original principal | true on understudy's internal calls made with the caller's identity (S3 or Secrets Manager to KMS), false otherwise (inferred; IAM-EVAL-34 records AWS's value) |
| `aws:CalledViaAWSMCP`, `aws:ViaAWSMCPService`, `aws:IsMcpServiceAction` | | MCP servers only | omitted; understudy has no MCP surface |
| `aws:CurrentTime` | Date | always | emulator clock (CLAUDE.md invariant 5), ISO 8601 UTC |
| `aws:EpochTime` | Date, Numeric | always | emulator clock, whole seconds |
| `aws:referer` | String | when the caller sends a `Referer` from a browser link | the `Referer` header when present (inferred) |
| `aws:RequestedRegion` | String | always; IAM calls "are always made to the us-east-1 Region" | the call's region; `us-east-1` for IAM and for the STS global endpoint (iam-sts-kms.md 4.2) |
| `aws:RequestTag/<k>`, `aws:TagKeys` | String single, String list | tags passed in the request; operations that accept tags | from each operation's tag input (per-operation table, 12.1) |
| `aws:SecureTransport` | Bool | always | per switch `iam.context.secure-transport` (8.2) |
| `aws:SourceAccount`, `aws:SourceArn` | String, ARN | service principal acting for a resource | the delivery's source (`access.Advise`) |
| `aws:SourceOrgID`, `aws:SourceOrgPaths` | | organization members only | never |
| `aws:UserAgent` | String | always | the `User-Agent` header |

Sensitive keys, for which "the use of wildcards ... does not have any valid use cases": `aws:PrincipalAccount`, `aws:PrincipalOrgID`, `aws:ResourceAccount`, `aws:ResourceOrgID`, `aws:SourceAccount`, `aws:SourceOrgID`, `aws:SourceVpc`, `aws:SourceVpce`, `aws:VpceAccount`, `aws:VpceOrgID` (S-KEYS `#condition-keys-sensitive`). The page gives no evaluation rule for them; they evaluate like any string key (inferred).

### 8.2 Keys understudy cannot supply honestly

- **TLS.** understudy is reached over plain HTTP on loopback (`AWS_ENDPOINT_URL`), while clients of AWS use TLS. A truthful `aws:SecureTransport` would be false and would trip the common "deny when SecureTransport is false" statement on every call. Switch `iam.context.secure-transport`: `assume-tls` (default, true; inferred) or `listener` (true only on a TLS listener).
- **Source IP.** `aws:SourceIp` "can only be used for public IP address ranges" (S-KEYS `#condition-keys-sourceip`); understudy sees loopback or private addresses. Switch `iam.context.source-ip`: `peer` (default, the TCP peer address; inferred) or `absent` (as if through a VPC endpoint).
- **VPC, organization and MFA keys**: never supplied (8.1); the absent-key rule then gives AWS's own answer for a public-endpoint request outside an organization, without MFA.
- **Service-specific keys** (`s3:prefix`, `s3:x-amz-server-side-encryption`, `kms:ViaService`, `sqs:...`): AWS supplies them per action (Service Authorization Reference, linked from S-COND); understudy supplies only those it implements. A condition on a key AWS would supply but understudy does not must never be assumed to hold: switch `iam.condition.unsupplied-key` `absent` (default: evaluate as absent and journal the key, operation and policy; inferred) or `assume-holds` (today's advisory behavior, for `iam.enforce: report` only).

## 9. Worked examples as test vectors

### 9.1 Vector format

- Decisions: `Allow`, `ImplicitDeny`, `ExplicitDeny`. Condition-level vectors give `cond=true` or `cond=false`; when the page prints a result column it is quoted.
- Basis: `example` (the page states this outcome for this input), `rule` (the page states the rule; the vector instantiates it with the page's or illustrative names), `derived` (computed from documented rules on the page's own policy; the page does not print it), `3p` (third-party-recorded).
- Accounts, names and ARNs are the pages'. `{{...}}` placeholders in the pages are written as their example values. Policies are compact JSON; a vector lists the policies in force by name.

### 9.2 Single-account evaluation (A)

Source S-ONE `#reference_policies_evaluation-logic_policies_evaluation_example`. Account 123456789012; caller `arn:aws:iam::123456789012:user/carlossalazar`.

```
A.id  {"Version":"2012-10-17","Statement":[{"Sid":"AllowS3ListRead","Effect":"Allow","Action":["s3:GetBucketLocation","s3:GetAccountPublicAccessBlock","s3:ListAccessPoints","s3:ListAllMyBuckets"],"Resource":"arn:aws:s3:::*"},{"Sid":"AllowS3Self","Effect":"Allow","Action":"s3:*","Resource":["arn:aws:s3:::amzn-s3-demo-bucket-carlossalazar/*","arn:aws:s3:::amzn-s3-demo-bucket-carlossalazar"]},{"Sid":"DenyS3Logs","Effect":"Deny","Action":"s3:*","Resource":"arn:aws:s3:::*log*"}]}
A.rp  (bucket policy on amzn-s3-demo-bucket-carlossalazar) {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:user/carlossalazar"},"Action":"s3:*","Resource":["arn:aws:s3:::amzn-s3-demo-bucket-carlossalazar/*","arn:aws:s3:::amzn-s3-demo-bucket-carlossalazar"]}]}
```

| ID | Action, resource | Policies | Expected | Basis |
|---|---|---|---|---|
| A1 | `s3:PutObject` `arn:aws:s3:::amzn-s3-demo-bucket-carlossalazar-logs/report.txt` | A.id | ExplicitDeny | example |
| A2 | `s3:PutObject` `arn:aws:s3:::amzn-s3-demo-bucket-carlossalazar/report.txt` | A.id, A.rp | Allow | example |
| A3 | as A2 | A.id only | Allow | rule ("If one of the policy types allows the request and the other doesn't, the request is still allowed") |
| A4 | as A2 | A.rp only | Allow | rule (same sentence; the bucket policy names the user, 2.3) |
| A5 | `s3:PutObject` `arn:aws:s3:::amzn-s3-demo-bucket-carlossalazar/catalog.txt` | A.id, A.rp | ExplicitDeny | derived (`*log*` matches `...carlossalazar/catalog.txt`; `*` spans `/`, S-RESOURCE) |

### 9.3 Explicit and implicit deny (B)

Source S-DENY. Resource `*`.

```
B.p      {"Version":"2012-10-17","Statement":[{"Sid":"AllowGetList","Effect":"Allow","Action":["iam:Get*","iam:List*"],"Resource":"*"},{"Sid":"DenyReports","Effect":"Deny","Action":"iam:*Report","Resource":"*"}]}
B.extra  {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:GenerateCredentialReport","Resource":"*"}]}
```

| ID | Action | Policies | Expected | Basis |
|---|---|---|---|---|
| B1 | `iam:GetUser` | B.p | Allow | example ("allows read-only access to IAM actions that begin with the prefixes Get and List") |
| B2 | `iam:CreatePolicy` | B.p | ImplicitDeny | example |
| B3 | `iam:GetOrganizationsAccessReport` | B.p | ExplicitDeny | example |
| B4 | `iam:GenerateCredentialReport` | B.p, B.extra | ExplicitDeny | example |

### 9.4 Cross-account (C, D)

C: source S-XACCT `#policies_evaluation_example-cross-account`. Caller: a session of `arn:aws:iam::111111111111:role/Demo` (session name illustrative: `arn:aws:sts::111111111111:assumed-role/Demo/carlos`). Buckets owned by 222222222222.

```
C.id  (role Demo) {"Version":"2012-10-17","Statement":[{"Sid":"AllowS3ListRead","Effect":"Allow","Action":"s3:ListAllMyBuckets","Resource":"*"},{"Sid":"AllowS3ProductionObjectActions","Effect":"Allow","Action":"s3:*Object*","Resource":"arn:aws:s3:::amzn-s3-demo-bucket-production/*"},{"Sid":"DenyS3Logs","Effect":"Deny","Action":"s3:*","Resource":["arn:aws:s3:::*log*","arn:aws:s3:::*log*/*"]}]}
C.rp  (bucket policy on amzn-s3-demo-bucket-production) {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject*","s3:PutObject*","s3:ReplicateObject","s3:RestoreObject"],"Principal":{"AWS":"arn:aws:iam::111111111111:role/Demo"},"Resource":"arn:aws:s3:::amzn-s3-demo-bucket-production/*"}]}
```

| ID | Action, resource | Policies | Expected | Basis |
|---|---|---|---|---|
| C1 | `s3:PutObject` `arn:aws:s3:::amzn-s3-demo-bucket-production-logs/report.txt` (no bucket policy) | C.id | ExplicitDeny | example |
| C2 | `s3:PutObject` `arn:aws:s3:::amzn-s3-demo-bucket-production/report.txt` | C.id, C.rp | Allow | example |
| C3 | `s3:DeleteObject` `arn:aws:s3:::amzn-s3-demo-bucket-production/report.txt` | C.id, C.rp | ImplicitDeny | example ("can create and edit, but not delete the objects") |
| C4 | `s3:PutBucketPolicy` `arn:aws:s3:::amzn-s3-demo-bucket-production` | C.id, C.rp | ImplicitDeny | example ("The role can't manage the bucket itself") |

D: source S-XRES `#access_policies-cross-account-delegating-resource-based-policies`. BucketA is owned by AccountA (id not given; vectors use 444455556666); AccountB is 111122223333 with users User1 and User2.

```
D.rp     (bucket policy on BucketA) {"Version":"2012-10-17","Statement":[{"Sid":"PrincipalAccess","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},"Action":"s3:*","Resource":"arn:aws:s3:::BucketA/*"}]}
D.user2  {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:Get*","s3:List*"],"Resource":"arn:aws:s3:::BucketA/*"}]}
D.full   {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}
```

| ID | Caller | Action, resource | Policies | Expected | Basis |
|---|---|---|---|---|---|
| D1 | root of 111122223333 | `s3:PutObject` `arn:aws:s3:::BucketA/k` | D.rp | Allow | example ("the root user has full access to BucketA") |
| D2 | User2 | `s3:GetObject` `arn:aws:s3:::BucketA/k` | D.rp, D.user2 | Allow | example |
| D3 | User2 | `s3:PutObject` `arn:aws:s3:::BucketA/k` | D.rp, D.user2 | ImplicitDeny | example |
| D4 | User1 | `s3:GetObject` `arn:aws:s3:::BucketA/k` | D.rp | ImplicitDeny | example |
| D5 | User2 | `s3:DeleteObject` `arn:aws:s3:::BucketA/k` | D.rp, D.full (on User2) | Allow | example ("User2 would automatically have access to any resources that AccountB has access to") |

### 9.5 Resource-policy principal forms (E)

Source S-ENF "Resource-based policies", S-POLICIES `#policies_bound` and `#policies_session`, S-PRINCIPAL `#principal-anonymous`. Account 111122223333; role `examplerole`; session `arn:aws:sts::111122223333:assumed-role/examplerole/examplerolesessionname`; user `exampleuser`; action `sqs:SendMessage` on `arn:aws:sqs:us-east-1:111122223333:q` (illustrative).

```
RP.session  {"Effect":"Allow","Principal":{"AWS":"arn:aws:sts::111122223333:assumed-role/examplerole/examplerolesessionname"},"Action":"sqs:SendMessage","Resource":"arn:aws:sqs:us-east-1:111122223333:q"}
RP.role     same with "Principal":{"AWS":"arn:aws:iam::111122223333:role/examplerole"}
RP.user     same with "Principal":{"AWS":"arn:aws:iam::111122223333:user/exampleuser"}
RP.acct     same with "Principal":{"AWS":"arn:aws:iam::111122223333:root"}
RP.starArn  same with "Principal":"*" and "Condition":{"ArnEquals":{"aws:PrincipalArn":"arn:aws:iam::111122223333:role/examplerole"}}
RP.star     same with "Principal":"*" and no Condition
ID.allow    {"Effect":"Allow","Action":"sqs:SendMessage","Resource":"arn:aws:sqs:us-east-1:111122223333:q"}
ID.deny     {"Effect":"Deny","Action":"sqs:SendMessage","Resource":"*"}
BND.narrow  boundary {"Effect":"Allow","Action":"s3:ListAllMyBuckets","Resource":"*"}
BND.deny    boundary [{"Effect":"Allow","Action":"*","Resource":"*"},{"Effect":"Deny","Action":"sqs:SendMessage","Resource":"*"}]
SP.narrow   session policy {"Effect":"Allow","Action":"s3:ListAllMyBuckets","Resource":"*"}
```

(Each fragment is one statement of a `2012-10-17` document.)

| ID | Caller | Policies | Expected | Basis |
|---|---|---|---|---|
| E1 | session | RP.session, BND.narrow, SP.narrow | Allow | rule |
| E2 | session | RP.role | Allow | rule (role flow chart skips the identity step) |
| E3 | session | RP.role, BND.narrow | ImplicitDeny | rule |
| E4 | session | RP.role, SP.narrow | ImplicitDeny | rule |
| E5 | user | RP.user, BND.narrow (on the user) | Allow | rule |
| E6 | user | RP.acct | ImplicitDeny | rule (account path goes to the identity check) |
| E7 | user | RP.acct, ID.allow | Allow | rule |
| E8 | session | RP.acct, ID.allow, BND.narrow | ImplicitDeny | rule |
| E9 | session | RP.starArn, BND.narrow, SP.narrow | Allow | rule |
| E10 | session | RP.starArn, ID.deny | ExplicitDeny | rule |
| E11 | session | RP.session, BND.deny | ExplicitDeny | rule ("an explicit deny in the permission boundary is effective") |
| E12 | session | ID.allow | Allow | rule |
| E13 | session | none | ImplicitDeny | rule |
| E14 | user | RP.user, BND.deny | ExplicitDeny | rule |
| E15 | user | RP.star | Allow | rule ("For IAM users and role principals within your account, no other permissions are required") |
| E16 | session | RP.star, BND.narrow | Allow per switch `iam.wildcard-principal.role-session` default; UNVERIFIED (IAM-EVAL-19) | inferred |

### 9.6 Permissions boundaries (F)

Source S-BOUND. Shirley vectors from the page's opening example; Zhang and Nikhil from `#access_policies_boundaries-delegate`. Account 123456789012.

```
F.shirleyB  {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*","cloudwatch:*","ec2:*"],"Resource":"*"}]}
F.shirleyI  {"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"iam:CreateUser","Resource":"*"}}
F.x  (XCompanyBoundaries) {"Version":"2012-10-17","Statement":[{"Sid":"ServiceBoundaries","Effect":"Allow","Action":["s3:*","cloudwatch:*","ec2:*","dynamodb:*"],"Resource":"*"},{"Sid":"AllowIAMConsoleForCredentials","Effect":"Allow","Action":["iam:ListUsers","iam:GetAccountPasswordPolicy"],"Resource":"*"},{"Sid":"AllowManageOwnPasswordAndAccessKeys","Effect":"Allow","Action":["iam:*AccessKey*","iam:ChangePassword","iam:GetUser","iam:*ServiceSpecificCredential*","iam:*SigningCertificate*"],"Resource":["arn:aws:iam::*:user/${aws:username}"]},{"Sid":"DenyS3Logs","Effect":"Deny","Action":"s3:*","Resource":["arn:aws:s3:::logs","arn:aws:s3:::logs/*"]},{"Sid":"DenyEC2Production","Effect":"Deny","Action":"ec2:*","Resource":"arn:aws:ec2:*:*:instance/i-1234567890abcdef0"}]}
F.zb (DelegatedUserBoundary) {"Version":"2012-10-17","Statement":[{"Sid":"CreateOrChangeOnlyWithBoundary","Effect":"Allow","Action":["iam:AttachUserPolicy","iam:CreateUser","iam:DeleteUserPolicy","iam:DetachUserPolicy","iam:PutUserPermissionsBoundary","iam:PutUserPolicy"],"Resource":"*","Condition":{"StringEquals":{"iam:PermissionsBoundary":"arn:aws:iam::123456789012:policy/XCompanyBoundaries"}}},{"Sid":"CloudWatchAndOtherIAMTasks","Effect":"Allow","Action":["cloudwatch:*","iam:CreateAccessKey","iam:CreateGroup","iam:CreateLoginProfile","iam:CreatePolicy","iam:DeleteGroup","iam:DeletePolicy","iam:DeletePolicyVersion","iam:DeleteUser","iam:GetAccountPasswordPolicy","iam:GetGroup","iam:GetLoginProfile","iam:GetPolicy","iam:GetPolicyVersion","iam:GetRolePolicy","iam:GetUser","iam:GetUserPolicy","iam:ListAccessKeys","iam:ListAttachedRolePolicies","iam:ListAttachedUserPolicies","iam:ListEntitiesForPolicy","iam:ListGroups","iam:ListGroupsForUser","iam:ListMFADevices","iam:ListPolicies","iam:ListPolicyVersions","iam:ListRolePolicies","iam:ListSSHPublicKeys","iam:ListServiceSpecificCredentials","iam:ListSigningCertificates","iam:ListUserPolicies","iam:ListUsers","iam:SetDefaultPolicyVersion","iam:SimulateCustomPolicy","iam:SimulatePrincipalPolicy","iam:UpdateGroup","iam:UpdateLoginProfile","iam:UpdateUser"],"NotResource":"arn:aws:iam::123456789012:user/Maria"},{"Sid":"NoBoundaryPolicyEdit","Effect":"Deny","Action":["iam:CreatePolicyVersion","iam:DeletePolicy","iam:DeletePolicyVersion","iam:SetDefaultPolicyVersion"],"Resource":["arn:aws:iam::123456789012:policy/XCompanyBoundaries","arn:aws:iam::123456789012:policy/DelegatedUserBoundary"]},{"Sid":"NoBoundaryUserDelete","Effect":"Deny","Action":"iam:DeleteUserPermissionsBoundary","Resource":"*"}]}
F.zi (DelegatedUserPermissions) {"Version":"2012-10-17","Statement":[{"Sid":"IAM","Effect":"Allow","Action":"iam:*","Resource":"*"},{"Sid":"CloudWatchLimited","Effect":"Allow","Action":["cloudwatch:GetDashboard","cloudwatch:GetMetricData","cloudwatch:ListDashboards","cloudwatch:GetMetricStatistics","cloudwatch:ListMetrics"],"Resource":"*"},{"Sid":"S3BucketContents","Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::ZhangBucket"}]}
F.iamFull  (IAMFullAccess, M-FULL `#IAMFullAccess-json`) {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["iam:*","organizations:DescribeAccount","organizations:DescribeOrganization","organizations:DescribeOrganizationalUnit","organizations:DescribePolicy","organizations:ListChildren","organizations:ListParents","organizations:ListPoliciesForTarget","organizations:ListRoots","organizations:ListPolicies","organizations:ListTargetsForPolicy"],"Resource":"*"}]}
F.s3ro     (AmazonS3ReadOnlyAccess, M-FULL `#AmazonS3ReadOnlyAccess-json`) {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:Get*","s3:List*","s3:Describe*","s3-object-lambda:Get*","s3-object-lambda:List*"],"Resource":"*"}]}
```

| ID | Caller, action, resource, context | Policies | Expected | Basis |
|---|---|---|---|---|
| F1 | Shirley, `iam:CreateUser` | boundary F.shirleyB, identity F.shirleyI | ImplicitDeny | example |
| F2 | Shirley, `s3:ListAllMyBuckets` | same | ImplicitDeny | example ("Shirley does not have permission to perform any operations in AWS") |
| F3 | Zhang, `iam:CreateUser` `arn:aws:iam::123456789012:user/Nikhil`, no `iam:PermissionsBoundary` | boundary F.zb, identity F.zi | ImplicitDeny | example (Task 4, first attempt) |
| F4 | as F3 with `iam:PermissionsBoundary` = `arn:aws:iam::123456789012:policy/XCompanyBoundaries` | same | Allow | example (Task 4, second attempt) |
| F5 | Zhang, `s3:ListBucket` `arn:aws:s3:::ZhangBucket` | same | ImplicitDeny | example |
| F6 | Zhang, `cloudwatch:GetDashboard` `*` | same | Allow | example |
| F7 | Zhang, `cloudwatch:PutDashboard` `*` | same | ImplicitDeny | derived (the identity policy allows five CloudWatch actions) |
| F8 | Zhang, `iam:DeleteUserPermissionsBoundary` `arn:aws:iam::123456789012:user/Nikhil` | same | ExplicitDeny | example |
| F9 | Zhang, `iam:SetDefaultPolicyVersion` `arn:aws:iam::123456789012:policy/XCompanyBoundaries` | same | ExplicitDeny | example |
| F10 | Zhang, `iam:CreateAccessKey` `arn:aws:iam::123456789012:user/Maria` | same | ImplicitDeny | example (NotResource) |
| F11 | Zhang, `iam:CreateAccessKey` `arn:aws:iam::123456789012:user/Nikhil` | same | Allow | example |
| F12 | Nikhil, `iam:ChangePassword` `arn:aws:iam::123456789012:user/Nikhil`, `aws:username`=Nikhil | boundary F.x, identity F.iamFull + F.s3ro | Allow | example ("he can change his own password") |
| F13 | Nikhil, `iam:CreateUser` `arn:aws:iam::123456789012:user/x` | same | ImplicitDeny | example |
| F14 | Nikhil, `iam:PutUserPolicy` `arn:aws:iam::123456789012:user/Nikhil` | same | ImplicitDeny | example ("or edit his policies") |
| F15 | Nikhil, `s3:GetObject` `arn:aws:s3:::any-bucket/k` | same | Allow | example ("read-only access to Amazon S3") |
| F16 | Nikhil, `s3:PutObject` `arn:aws:s3:::logs/k`; bucket policy on `logs` allows `arn:aws:iam::123456789012:user/Nikhil` `s3:PutObject` | same plus that bucket policy | ExplicitDeny | example |
| F17 | Nikhil, `secretsmanager:GetSecretValue` on a secret whose resource policy allows `arn:aws:iam::123456789012:user/Nikhil` | same plus that resource policy | Allow | example |
| F18 | Nikhil, `iam:ChangePassword` `arn:aws:iam::123456789012:user/Other`, `aws:username`=Nikhil | same | ImplicitDeny | derived (`${aws:username}` resource) |

### 9.7 Session policies (G)

Source S-ARPERM. Caller: a session of a role in the bucket owner's account.

```
G.role     {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::productionapp"},{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject"],"Resource":"arn:aws:s3:::productionapp/*"}]}
G.session  {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::productionapp"},{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject"],"Resource":"arn:aws:s3:::productionapp/*"}]}
G.bucket   {"Version":"2012-10-17","Statement":{"Principal":{"AWS":"*"},"Effect":"Deny","Action":"s3:DeleteObject","Resource":"arn:aws:s3:::productionapp/*"}}
```

| ID | Action, resource | Policies | Expected | Basis |
|---|---|---|---|---|
| G1 | `s3:DeleteObject` `arn:aws:s3:::productionapp/k` | G.role, no session policy | Allow | example (`#permissions-assume-role-example-role-access-policy`) |
| G2 | same | G.role, G.session | ImplicitDeny | example (`#permissions-assume-role-example-passed-policy`) |
| G3 | `s3:PutObject` `arn:aws:s3:::productionapp/k` | G.role, G.session | Allow | example |
| G4 | `s3:DeleteObject` `arn:aws:s3:::productionapp/k` | G.role, G.bucket | ExplicitDeny | example (`#permissions-assume-role-example-resource-based-policy`) |

### 9.8 AssumeRole and trust policies (H)

Sources STS-AR, S-STSCMP, S-TAGS, K-EVAL `#example-cross-acct`. Role `arn:aws:iam::111122223333:role/R`; user U `arn:aws:iam::111122223333:user/U`; external user X `arn:aws:iam::444455556666:user/X`; action `sts:AssumeRole` on the role ARN.

```
T.user     {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:user/U"},"Action":"sts:AssumeRole"}]}
T.acct     same with "Principal":{"AWS":"arn:aws:iam::111122223333:root"}
T.ext      same with "Principal":{"AWS":"arn:aws:iam::444455556666:root"}
T.extUser  same with "Principal":{"AWS":"arn:aws:iam::444455556666:user/X"}
T.lambda   same with "Principal":{"Service":"lambda.amazonaws.com"}
T.mfa      T.acct plus "Condition":{"Bool":{"aws:MultiFactorAuthPresent":true}}
T.tags     T.user with "Action":["sts:AssumeRole","sts:TagSession"]
I.assume   {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Resource":"arn:aws:iam::111122223333:role/R"}]}
```

| ID | Caller | Policies | Expected | Basis |
|---|---|---|---|---|
| H1 | U | T.user | Allow | rule |
| H2 | U | T.acct, I.assume on U | Allow | rule |
| H3 | U | T.acct | ImplicitDeny | rule |
| H4 | X | T.ext, I.assume on X | Allow | rule |
| H5 | X | T.ext | ImplicitDeny | rule |
| H6 | X | T.extUser | ImplicitDeny | rule ("must also have permissions that are delegated from the account administrator") |
| H7 | U | T.lambda, I.assume on U | ImplicitDeny | rule (a role must allow explicitly, 3.1) |
| H8 | root of 111122223333 | T.acct | refused (S-STSCMP); wire UNVERIFIED (IAM-EVAL-22) | rule |
| H9 | U with long-term keys | T.mfa, I.assume on U | ImplicitDeny | rule (key absent for access keys, S-KEYS; STS-AR MFA paragraph) |
| H10 | U passing `Tags` | T.user (no `sts:TagSession`) | ImplicitDeny | rule ("the AssumeRole operation fails") |
| H11 | U passing `Tags` | T.tags | Allow | derived |
| H12 | `arn:aws:iam::111122223333:user/bob` on role `Engineering`, trust names bob | trust only | Allow | example (K-EVAL) |

### 9.9 KMS (I)

Sources K-EVAL `#example-no-iam`, `#example-cross-acct`, K-DET, K-POL, K-OVR, K-XACCT. Same-account key `arn:aws:kms:us-west-2:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab` unless stated; the cross-account key is `arn:aws:kms:us-west-2:444455556666:key/1234abcd-12ab-34cd-56ef-1234567890ab`.

```
K.default   {"Version":"2012-10-17","Id":"key-test-1","Statement":[{"Sid":"DelegateToIAMPolicies","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},"Action":"kms:*","Resource":"*"}]}
K.noAcct    [{"Sid":"Allow access for Key Administrators","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:role/KMSKeyAdmins"},"Action":["kms:Describe*","kms:Put*","kms:Create*","kms:Update*","kms:Enable*","kms:Revoke*","kms:List*","kms:Disable*","kms:Get*","kms:Delete*","kms:ScheduleKeyDeletion","kms:CancelKeyDeletion"],"Resource":"*"},{"Sid":"Allow use of the key","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:role/EncryptionApp"},"Action":["kms:DescribeKey","kms:GenerateDataKey*","kms:Encrypt","kms:ReEncrypt*","kms:Decrypt"],"Resource":"*"},{"Sid":"Allow attachment of persistent resources","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:role/EncryptionApp"},"Action":["kms:ListGrants","kms:CreateGrant","kms:RevokeGrant"],"Resource":"*","Condition":{"Bool":{"kms:GrantIsForAWSResource":true}}}]
K.acct2     {"Id":"key-policy-acct-2","Version":"2012-10-17","Statement":[{"Sid":"PermissionUseIAMpolicies","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::444455556666:root"},"Action":"kms:*","Resource":"*"},{"Sid":"AllowAccount1UseThisKMSKeys","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},"Action":["kms:Encrypt","kms:Decrypt","kms:ReEncryptFrom","kms:ReEncryptTo","kms:GenerateDataKey","kms:GenerateDataKeyWithoutPlaintext","kms:DescribeKey"],"Resource":"*"}]}
K.engIam    (on role Engineering, 111122223333) {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["kms:Encrypt","kms:Decrypt","kms:ReEncryptFrom","kms:ReEncryptTo","kms:GenerateDataKey","kms:GenerateDataKeyWithoutPlaintext","kms:DescribeKey"],"Resource":["arn:aws:kms:us-west-2:444455556666:key/1234abcd-12ab-34cd-56ef-1234567890ab"]}]}
K.star      [{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"kms:Decrypt","Resource":"*"}]
```

(K.noAcct and K.star are statement lists of a `2012-10-17` key policy; K.noAcct is statements 2 to 4 of K-DET.)

| ID | Caller, action | Policies | Expected | Basis |
|---|---|---|---|---|
| I1 | user Alice (111122223333), `kms:Encrypt` | K.default, no IAM policy | ImplicitDeny | example |
| I2 | Alice, `kms:Encrypt` | K.default, IAM Allow `kms:Encrypt` on the key | Allow | derived |
| I3 | Alice, `kms:Encrypt` | K.noAcct, IAM Allow `kms:Encrypt` on the key | ImplicitDeny | rule (K-POL) |
| I4 | Alice, `kms:Encrypt` | key policy Allow `user/Alice` `kms:Encrypt`, IAM Deny `kms:Encrypt` | ExplicitDeny | rule (K-POL) |
| I5 | session of `role/EncryptionApp`, `kms:Decrypt` | K.noAcct, no IAM policy | Allow | rule (key policy names the caller's role, same account) |
| I6 | root of 111122223333, `kms:Decrypt` | K.noAcct | ImplicitDeny | rule (K-POL) |
| I7 | root of 111122223333, `kms:Decrypt` | K.default | Allow | derived (account principal plus root's full access) |
| I8 | session of `role/Engineering` (111122223333), `kms:Decrypt` on the 444455556666 key | K.acct2, K.engIam | Allow | example |
| I9 | same | K.acct2 only | ImplicitDeny | example ("no one in account 1 can use the KMS key in account 2 until account 1 defines IAM policies") |
| I10 | same, `kms:ScheduleKeyDeletion` | key policy granting 111122223333 `kms:*`, IAM Allow `kms:*` on the key | denied (K-XACCT); error per `kms.cross-account.management-error`, UNVERIFIED (IAM-EVAL-30) | rule |
| I11 | user in 444455556666 with IAM Allow `kms:Decrypt` on the key, `kms:Decrypt` | K.star | Allow | rule (K-OVR) |
| I12 | same without the IAM policy | K.star | ImplicitDeny | rule (K-OVR, K-XACCT) |
| I13 | session of `role/EncryptionApp`, `kms:CreateGrant`, `kms:GrantIsForAWSResource`=true | K.noAcct | Allow | derived (grants are unbound in understudy; condition-level) |
| I14 | same with `kms:GrantIsForAWSResource`=false | K.noAcct | ImplicitDeny | derived |

### 9.10 Condition operators (J)

Source S-OPS, anchor per group. Each policy fragment is the Condition block of an Allow (or, where marked, Deny) statement; "Match" columns are quoted from the page.

| ID | Condition | Request context | Page result | Expected | Basis |
|---|---|---|---|---|---|
| J1 | `{"StringEquals":{"aws:PrincipalTag/job-category":"iamuser-admin"}}` | `iamuser-admin` | Match | cond=true | example `#Conditions_String` |
| J2 | same | `dev-ops` | No match | cond=false | example |
| J3 | same | absent | No match | cond=false | example |
| J4 | `{"StringLike":{"s3:prefix":["home/","home/${aws:username}/"]}}` | `aws:username`=martha_rivera | values become `home/`, `home/martha_rivera/` | substitution | example |
| J5 | same | `aws:username`=nikki_wolf | values become `home/`, `home/nikki_wolf/` | substitution | example |
| J6 | same | `aws:username` absent (`s3:prefix` not stated) | No match | cond=false for `s3:prefix`=`home/x/`; for `s3:prefix`=`home/` UNVERIFIED (IAM-EVAL-16) | example |
| J7 | `{"StringEqualsIgnoreCase":{"aws:RequestTag/environment":["preprod","storage"]},"ForAnyValue:StringEquals":{"aws:TagKeys":"environment"}}` | TagKeys [environment]; RequestTag/environment preprod | Match | cond=true | example `#conditions_string_multivalued` |
| J8 | same | TagKeys [environment, costcenter]; RequestTag/environment PreProd | Match | cond=true | example |
| J9 | same | TagKeys [Environment]; RequestTag/Environment preprod | No match | cond=false | example |
| J10 | same | TagKeys [costcenter]; RequestTag/environment preprod | No match | cond=false | example |
| J11 | same | TagKeys absent; RequestTag/environment storage | No match | cond=false | example |
| J12 | same | TagKeys [environment]; RequestTag/environment absent | No match | cond=false | example |
| J13 | same | both absent | No match | cond=false | example |
| J14 | Deny `{"ForAllValues:StringNotLike":{"aws:ResourceOrgPaths":["o-acorg/r-acroot/ou-acroot-mediaou/","o-acorg/r-acroot/ou-acroot-sportsou/*"]}}` | [o-acorg/r-acroot/ou-acroot-sportsou/costcenter/] | Match (see 11, C1) | cond=false: not denied | derived `#Conditions_String-wildcard` |
| J15 | same | [o-acorg/r-acroot/ou-acroot-mediaou/costcenter/] | No match (see 11, C1) | cond=true: ExplicitDeny | derived |
| J16 | same | absent | No match (see 11, C1) | cond=true: ExplicitDeny | derived |
| J17 | `{"NumericLessThanEquals":{"s3:max-keys":"10"}}` | 10 | | cond=true | derived ("list up to 10 objects") `#Conditions_Numeric` |
| J18 | same | 11 | | cond=false | derived |
| J19 | `{"DateGreaterThan":{"aws:TokenIssueTime":"2020-01-01T00:00:01Z"}}` | absent (access keys) | denied | cond=false | example `#Conditions_Date` |
| J20 | same | 2020-06-01T00:00:00Z | | cond=true | derived |
| J21 | Deny `{"Bool":{"aws:SecureTransport":"false"}}` | false | Match | cond=true | example `#Conditions_Boolean` |
| J22 | same | true | No match | cond=false | example |
| J23 | same | absent | No match | cond=false | example |
| J24 | `{"BinaryEquals":{"key":"QmluYXJ5VmFsdWVJbkJhc2U2NA=="}}` | QmluYXJ5VmFsdWVJbkJhc2U2NA== | Match | cond=true | example `#Conditions_BinaryEquals` |
| J25 | same | ASIAIOSFODNN7EXAMPLE | No match | cond=false | example |
| J26 | same | absent | No match | cond=false | example |
| J27 | `{"IpAddress":{"aws:SourceIp":"203.0.113.0/24"}}` | 203.0.113.1 | Match | cond=true | example `#Conditions_IPAddress` |
| J28 | same | 198.51.100.1 | No match | cond=false | example |
| J29 | `{"IpAddress":{"aws:SourceIp":["203.0.113.0/24","2001:DB8:1234:5678::/64"]}}` | 2001:db8:1234:5678::1 | | cond=true | derived |
| J30 | `{"ArnEquals":{"aws:SourceArn":"arn:aws:sns:us-west-2:123456789012:TOPIC-ID"}}` | arn:aws:sns:us-west-2:123456789012:TOPIC-ID | Match | cond=true | example `#Conditions_ARN` |
| J31 | same | arn:aws:sns:us-west-2:777788889999:TOPIC-ID | No match | cond=false | example |
| J32 | same | absent | No match | cond=false | example |
| J33 | `{"ForAllValues:ArnLike":{"logs:LogGeneratingResourceArns":["arn:aws:cloudfront::123456789012:distribution/*","arn:aws:cloudfront::123456789012:distribution/support*"]}}` | [arn:aws:cloudfront::123456789012:distribution/costcenter] | Match | cond=true | example `#conditions_arn_multivalued` (ARN spelling per 11, C2) |
| J34 | same | [...distribution/costcenter, ...distribution/support2025] | Match | cond=true | example |
| J35 | same | [...distribution/costcenter, ...distribution/admin] | Match | cond=true | example |
| J36 | same | [arn:aws:cloudfront::777788889999:distribution/costcenter] | No match | cond=false | example |
| J37 | same | absent | Match | cond=true | example |
| J38 | `{"StringLikeIfExists":{"ec2:InstanceType":["t1.*","t2.*","m3.*"]}}` | t1.micro | Match | cond=true | example `#Conditions_IfExists` |
| J39 | same | m2.micro | No match | cond=false | example |
| J40 | same | absent | Match | cond=true | example |
| J41 | `{"StringLike":{"ec2:InstanceType":["t1.*","t2.*","m3.*"]}}` | absent | statement fails | cond=false | example (the `THISPOLICYDOESNOTWORK` policy) |
| J42 | `{"Null":{"aws:TokenIssueTime":"false"}}` | present | | cond=true | derived ("the user must be using temporary credentials") `#Conditions_Null` |
| J43 | same | absent | | cond=false | derived |

### 9.11 Multiple keys and values (K)

Source S-MULTI. Both policies: bucket policy statement, `Principal` `arn:aws:iam::222222222222:root`, `s3:ListBucket` on `arn:aws:s3:::amzn-s3-demo-bucket`.

```
K.and  {"StringEquals":{"aws:PrincipalTag/department":["finance","hr","legal"],"aws:PrincipalTag/role":["audit","security"]},"ArnLike":{"aws:PrincipalArn":["arn:aws:iam::222222222222:user/Ana","arn:aws:iam::222222222222:user/Mary"]}}
K.nor  same with "ArnNotLike" in place of "ArnLike"
```

| ID | Condition | department, role, PrincipalArn | Page result | Expected | Anchor |
|---|---|---|---|---|---|
| K1 | K.and | legal, audit, user/Mary | Match | cond=true | `#reference_policies_multiple-conditions-eval` |
| K2 | K.and | hr, audit, user/Nikki | No match | cond=false | same |
| K3 | K.and | hr, payroll, user/Mary | No match | cond=false | same |
| K4 | K.and | hr, absent, user/Mary | No match | cond=false | same |
| K5 | K.and | absent, absent, user/Mary | No match | cond=false | same |
| K6 | K.nor | legal, audit, user/Nikki | Match | cond=true | `#reference_policies_multiple-conditions-negated-matching-eval` |
| K7 | K.nor | hr, audit, user/Mary | No match | cond=false | same |
| K8 | K.nor | hr, payroll, user/Nikki | No match | cond=false | same |
| K9 | K.nor | hr, absent, user/Nikki | No match | cond=false | same |
| K10 | K.nor | absent, absent, user/Nikki | No match | cond=false | same |

All rows: basis example.

### 9.12 Multivalued keys (L, M)

Sources S-SETS (L) and S-MVEX (M). `aws:TagKeys` values listed; "absent" means no `aws:TagKeys`. All rows: basis example.

```
L.all  Allow ec2:DeleteTags {"ForAllValues:StringEquals":{"aws:TagKeys":["environment","cost-center"]},"Null":{"aws:TagKeys":"false"}}
L.any  Allow ec2:DeleteTags {"ForAnyValue:StringEquals":{"aws:TagKeys":["environment","cost-center"]}}
M.one  Deny iam:Tag*,iam:UnTag* {"ForAllValues:StringNotLike":{"aws:TagKeys":"key1*"}}
M.two  Deny iam:Tag*,iam:UnTag* {"ForAllValues:StringNotLike":{"aws:TagKeys":["key1*","key2*"]}}
M.snap Deny ec2:CreateSnapshot,ec2:CreateSnapshots on arn:aws:ec2:us-west-2::snapshot/* {"ForAnyValue:StringEquals":{"aws:TagKeys":"webserver"}}
```

| ID | Condition | TagKeys | Page result | Expected | Anchor |
|---|---|---|---|---|---|
| L1 | L.all | [environment] | Match | cond=true | `#reference_policies_condition-forallvalues-example` |
| L2 | L.all | [cost-center] | Match | cond=true | same |
| L3 | L.all | [environment, cost-center] | Match | cond=true | same |
| L4 | L.all | [environment, dept] | No match | cond=false | same |
| L5 | L.all | absent | No match | cond=false (the Null clause) | same |
| L6 | L.any | [environment] | Match | cond=true | `#reference_policies_condition-foranyvalue-example` |
| L7 | L.any | [cost-center] | Match | cond=true | same |
| L8 | L.any | [environment, cost-center] | Match | cond=true | same |
| L9 | L.any | [environment, dept] | Match | cond=true | same |
| L10 | L.any | [dept] | No match | cond=false | same |
| L11 | L.any | absent | No match | cond=false | same |
| M1 | M.one | [key1:legal] | No match | cond=false: not denied | `#reference_policies_condition_examples-multi-valued-context-keys-1` |
| M2 | M.one | [key1:hr, key1:personnel] | No match | cond=false: not denied | same |
| M3 | M.one | [key2:audit] | Match | cond=true: ExplicitDeny | same |
| M4 | M.one | absent | Match | cond=true: ExplicitDeny | same |
| M5 | M.two | [key1:legal] | No match | cond=false | same |
| M6 | M.two | [key1:hr, key1:personnel] | No match | cond=false | same |
| M7 | M.two | [key1:hr, key2:audit] | No match | cond=false | same |
| M8 | M.two | [key3:legal] | Match | cond=true: ExplicitDeny | same |
| M9 | M.two | absent | Match | cond=true: ExplicitDeny | same |
| M10 | M.snap | [webserver] | Match | cond=true: ExplicitDeny | `#reference_policies_condition_examples-multi-valued-context-keys-2` |
| M11 | M.snap | [environment, webserver, test] | Match | cond=true: ExplicitDeny | same |
| M12 | M.snap | [environment, test] | No match | cond=false | same |
| M13 | M.snap | absent | No match | cond=false | same |

### 9.13 Single-valued keys and the request context (N, O)

N: source S-SVEX. O: source S-CTX `#reqcontext_example`.

```
N.ec2  [{"Effect":"Allow","Action":"ec2:CreateVolume","Resource":"*"},{"Effect":"Allow","Action":"ec2:CreateTags","Resource":"arn:aws:ec2:us-east-1:123456789012:volume/*","Condition":{"StringLike":{"aws:RequestTag/project":"*"}}},{"Effect":"Allow","Action":"ec2:CreateTags","Resource":"arn:aws:ec2:us-east-1:123456789012:*/*","Condition":{"StringNotEquals":{"aws:ResourceTag/environment":"production"}}}]
N.ecs  {"StringEquals":{"aws:RequestTag/environment":["production","prod-backup"]},"ArnEquals":{"ecs:cluster":["arn:aws:ecs:us-east-1:111122223333:cluster/default1","arn:aws:ecs:us-east-1:111122223333:cluster/default2"]}}
O.p    {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:CreateBucket","Resource":"arn:aws:s3:::amzn-s3-demo-bucket1","Condition":{"StringEquals":{"aws:PrincipalTag/dept":"123"}}}]}
```

| ID | Request | Policies | Page result | Expected | Basis |
|---|---|---|---|---|---|
| N1 | context `aws:RequestTag/environment`=production, `ecs:cluster`=...cluster/default1 | N.ecs | Match | cond=true | example `#reference_policies_condition_examples-single-valued-context-keys-2` |
| N2 | prod-backup, ...cluster/default2 | N.ecs | Match | cond=true | example |
| N3 | `aws:RequestTag/webserver`=production, ...cluster/default2 | N.ecs | No match | cond=false | example |
| N4 | no RequestTag, ...cluster/default2 | N.ecs | No match | cond=false | example |
| N5 | `ec2:CreateTags` on `arn:aws:ec2:us-east-1:123456789012:volume/vol-1`, `aws:RequestTag/project`=Feature3 | N.ec2 | created | Allow | example `#reference_policies_condition_examples-single-valued-context-keys-1` |
| O1 | `s3:CreateBucket` `arn:aws:s3:::amzn-s3-demo-bucket1`, dept=123 | O.p | Match | Allow | example |
| O2 | `s3:DeleteBucket` same bucket, dept=123 | O.p | No match | ImplicitDeny | example |
| O3 | `s3:CreateBucket`, dept=321 | O.p | No match | ImplicitDeny | example |
| O4 | `s3:CreateBucket`, no PrincipalTag | O.p | No match | ImplicitDeny | example |

### 9.14 Key and value case (P)

| ID | Condition | Context | Expected | Basis | Source |
|---|---|---|---|---|---|
| P1 | `{"StringEquals":{"aws:username":"john"}}` | `aws:username`=john | cond=true | example | S-COND |
| P2 | same | John | cond=false ("Users named John are denied access") | example | S-COND |
| P3 | `{"StringEqualsIgnoreCase":{"aws:username":"john"}}` | John | cond=true | example | S-COND |
| P4 | `{"IpAddress":{"aws:SourceIP":"203.0.113.0/24"}}` | `aws:SourceIp`=203.0.113.5 | cond=true (key names case-insensitive) | rule | S-COND |
| P5 | `{"NumericLessThanEquals":{"aws:MultiFactorAuthAge":"3600"}}` | 2800 | cond=true | example | S-COND `#AccessPolicyLanguage_RequestContext` |
| P6 | same | absent (access keys) | cond=false ("it won't match") | example | same |
| P7 | `{"StringEquals":{"aws:ResourceTag/Owner":"${aws:username}"}}` | user richard; resource tag `Owner`=richard | cond=true | example | S-ABAC `#access_tags_control-resources` |
| P8 | same | resource tag `owner`=richard | cond=true ("The tag key Owner matches both Owner and owner") | example | same |
| P9 | same | resource tag `Owner`=Richard | cond=false | derived (StringEquals is case-sensitive) | S-OPS |

### 9.15 Policy variables (Q)

Source S-VARS unless stated; account 111122223333 unless stated.

| ID | Policy element | Context | Request resource | Expected | Basis |
|---|---|---|---|---|---|
| Q1 | `2012-10-17`, Resource `arn:aws:dynamodb:us-east-2:111122223333:table/${aws:username}` | `aws:username`=bob | `arn:aws:dynamodb:us-east-2:111122223333:table/bob` | matches | rule (S-RESOURCE `#reference_policies_elements_resource_policy-variables`) |
| Q2 | same | bob | `...table/alice` | no match | rule |
| Q3 | same policy with `"Version":"2008-10-17"` | bob | `...table/bob` | no match (literal `${aws:username}`) | rule (S-VERSION) |
| Q4 | same policy with no `Version` | bob | `...table/bob` | no match | rule (`#policy-vars-using-variables`) |
| Q5 | Deny `s3:GetObject` on `arn:aws:s3:::amzn-s3-demo-bucket/*`, `{"StringNotEquals":{"s3:ExistingObjectTag/Team":"${aws:PrincipalTag/Team}"}}` | no PrincipalTag/Team; object tag Team=blue | `arn:aws:s3:::amzn-s3-demo-bucket/k` | ExplicitDeny ("Access is explicitly denied when aws:principaltag/Team is not set") | example (`#policy-vars-no-value`) |
| Q6 | same | PrincipalTag/Team=blue; object tag Team=blue | same | not denied | derived |
| Q7 | Resource `arn:aws:s3:::amzn-s3-demo-bucket-${aws:PrincipalTag/team, 'company-wide'}` | team=yellow | `arn:aws:s3:::amzn-s3-demo-bucket-yellow` | matches | example (`#policy-vars-default-values`) |
| Q8 | same | no team tag | `arn:aws:s3:::amzn-s3-demo-bucket-company-wide` | matches | example |
| Q9 | Resource `arn:aws:s3:::amzn-s3-demo-bucket/${aws:PrincipalTag/team}/*` | no team tag | any object | no match | rule (`#policy-vars-no-value`) |
| Q10 | Resource `arn:aws:sqs:us-east-2:111122223333:${aws:PrincipalTag/team}-queue` | team=qa | `arn:aws:sqs:us-east-2:111122223333:qa-queue` | matches | example (S-ABAC `#access_tags_control-resources`) |
| Q11 | `{"StringLike":{"s3:prefix":["${aws:PrincipalTag/team}/*"]}}` | team=blue; `s3:prefix`=blue/x | | cond=true | derived (`#policy-vars-using-variables`) |
| Q12 | `{"StringEquals":{"s3:ExistingObjectTag/owner":"${aws:PrincipalTag/owner}"}}` | PrincipalTag/owner=ana; object tag owner=ana | | cond=true | derived (same section) |
| Q13 | Resource `arn:aws:s3:::b/a${*}b` (`2012-10-17`) | | `arn:aws:s3:::b/a*b` | matches; `arn:aws:s3:::b/axb` does not | rule (`#policy-vars-specialchars`); detail IAM-EVAL-12 |
| Q14 | Resource `arn:aws:sns:*:*:${aws:PrincipalTag/access-project}-${aws:PrincipalTag/access-application}-${aws:PrincipalTag/access-environment}-*` | tags alpha, web, prod | `arn:aws:sns:us-east-1:111122223333:alpha-web-prod-topic1` | matches | derived (`#policy-vars-resourceelement`) |

### 9.16 Action and resource patterns (R)

Sources S-ACTION, S-RESOURCE `#reference_policies_elements_resource_wildcards`, S-ARNS `#arns-paths-wildcards`, S-GRAMMAR. All `2012-10-17`.

| ID | Pattern | Value | Match | Basis |
|---|---|---|---|---|
| R1 | action `iam:*AccessKey*` | `iam:CreateAccessKey` | yes | example |
| R2 | same | `iam:DeleteAccessKey` | yes | example |
| R3 | same | `iam:ListAccessKeys` | yes | example |
| R4 | same | `iam:UpdateAccessKey` | yes | example |
| R5 | action `IAM:listaccesskeys` | `iam:ListAccessKeys` | yes | example |
| R6 | action `SQS:SendMessage` | `sqs:SendMessage` | yes | rule (S-OPS `#Conditions_ARN` example uses `SQS:SendMessage`) |
| R7 | action `s3:*` | `s3:GetObject` | yes | example |
| R8 | resource `arn:aws:s3:::amzn-s3-demo-bucket/*/test/*` | `arn:aws:s3:::amzn-s3-demo-bucket/1/test/object.jpg` | yes | example |
| R9 | same | `.../1/2/test/object.jpg` | yes | example |
| R10 | same | `.../1/2/test/3/object.jpg` | yes | example |
| R11 | same | `.../1/2/3/test/4/object.jpg` | yes | example |
| R12 | same | `.../1///test///object.jpg` | yes | example |
| R13 | same | `.../1/test/.jpg` | yes | example |
| R14 | same | `arn:aws:s3:::amzn-s3-demo-bucket//test/object.jpg` | yes | example |
| R15 | same | `.../1/test/` | yes | example |
| R16 | same | `.../1-test/object.jpg` | no | example |
| R17 | same | `.../test/object.jpg` | no | example |
| R18 | same | `.../1/2/test.jpg` | no | example |
| R19 | resource `arn:aws:iam::123456789012:role/*` | `arn:aws:iam::123456789012:role/anything` | yes | example |
| R20 | resource `arn:aws:iam::123456789012:role/service-*` | `.../role/service-role1`, `.../role/service-test` | yes | example |
| R21 | resource `arn:aws:s3:::amzn-s3-demo-bucket/????-test` | `.../1234-test`, `.../2024-test`, `.../a100-test` | yes | example |
| R22 | resource `arn:aws:iam::123456789012:user/accounting/*` | `arn:aws:iam::123456789012:user/accounting/ana` | yes | example |
| R23 | identity-policy resource `arn:aws:sqs` | `arn:aws:sqs:us-east-1:111122223333:q` | yes (completed to `arn:aws:sqs:*:*:*`) | example (S-GRAMMAR) |
| R24 | resource `arn:aws:iam::111122223333:user/Bob` | `arn:aws:iam::111122223333:user/bob` | no | rule ("the IAM user name is case sensitive") |
| R25 | resource `arn:aws:qbusiness:*:123456789012:*` | `arn:aws:qbusiness:us-east-1:123456789012:application/a1` | yes | example (S-ARNS) |

### 9.17 NotAction, NotResource, NotPrincipal and wildcard principals (S)

| ID | Policies | Request | Expected | Basis | Source |
|---|---|---|---|---|---|
| S1 | Allow `"NotAction":"s3:DeleteBucket"`, `"Resource":"arn:aws:s3:::*"` | `s3:DeleteBucket` `arn:aws:s3:::b` | ImplicitDeny | example | S-NOTACTION |
| S2 | same | `s3:ListBucket` `arn:aws:s3:::b` | Allow | example | same |
| S3 | same | `sqs:SendMessage` `arn:aws:sqs:us-east-1:111122223333:q` | ImplicitDeny ("other service actions are not applicable to the S3 resources") | example | same |
| S4 | Allow `"NotAction":"iam:*"`, `"Resource":"*"` | `sqs:SendMessage` on a queue | Allow | example | same |
| S5 | same | `iam:CreateUser` | ImplicitDeny | example | same |
| S6 | Allow `*` on `*` plus Deny `"NotAction":"iam:*"`, `"Resource":"*"`, `{"BoolIfExists":{"aws:MultiFactorAuthPresent":"false"}}` | `s3:GetObject`, MFA false | ExplicitDeny | example | same |
| S7 | same | `s3:GetObject`, MFA true | Allow ("the final Deny statement has no effect") | example | same |
| S8 | same | `s3:GetObject`, key absent | ExplicitDeny (IfExists) | derived | S-OPS `#Conditions_IfExists` |
| S9 | same | `iam:ListUsers`, MFA false | Allow | example | S-NOTACTION |
| S10 | Allow `s3:*` on `*` plus Deny `s3:*` `"NotResource":["arn:aws:s3:::HRBucket/Payroll","arn:aws:s3:::HRBucket/Payroll/*"]` | `s3:GetObject` `arn:aws:s3:::HRBucket/Payroll/x` | Allow | example | S-NOTRES |
| S11 | same | `s3:GetObject` `arn:aws:s3:::HRBucket/Other/x` | ExplicitDeny | example | same |
| S12 | the Deny statement of S10 alone | `s3:GetObject` `arn:aws:s3:::HRBucket/Payroll/x` | ImplicitDeny ("this policy does not grant the user access to any resources") | example | same |
| S13 | resource policy Deny `"NotPrincipal":{"AWS":[<caller's ARN>, <caller's account>]}`; caller has a permissions boundary | any covered action | ExplicitDeny ("will always deny any IAM principal that has a permissions boundary") | rule | S-NOTPRIN, S-BOUND |
| S14 | resource policy Deny `"Principal":"*"`, `s3:*`, `{"ArnNotEquals":{"aws:PrincipalArn":"arn:aws:iam::444455556666:user/user-name"}}` | caller `arn:aws:iam::444455556666:user/user-name` | not denied | rule | S-PRINCIPAL `#principal-anonymous` |
| S15 | same | another user | ExplicitDeny | rule | same |
| S16 | same | anonymous (`aws:PrincipalArn` absent) | ExplicitDeny (negated operator, absent key) | derived | S-OPS |
| S17 | Deny `"Principal":"*"`, `s3:*`, `{"StringNotEqualsIfExists":{"aws:PrincipalServiceName":"codebuild.amazonaws.com"}}` | service principal codebuild.amazonaws.com | not denied | example | S-NOTPRIN `#notprincipal-alternative-service-principal` |
| S18 | same | service principal sns.amazonaws.com | ExplicitDeny | derived | same |
| S19 | same | an IAM user (key absent) | ExplicitDeny ("the request is still denied even if the condition key is not present") | derived | S-OPS `#Conditions_IfExists` |
| S20 | Deny `"Principal":"*"` `s3:PutObject`, `{"StringNotEqualsIfExists":{"aws:SourceVpc":"vpc-111bbb22"},"BoolIfExists":{"aws:PrincipalIsAWSService":"false"}}` | IAM user, public endpoint | ExplicitDeny | derived | S-KEYS `#condition-keys-principalisawsservice` |
| S21 | same | IAM user, `aws:SourceVpc`=vpc-111bbb22 | not denied | example ("restricted unless the request originates from vpc-111bbb22") | same |
| S22 | same | service principal cloudtrail.amazonaws.com | not denied | example ("or is from a service principal") | same |
| S23 | Deny `"Principal":"*"` `s3:*`, `{"StringNotEqualsIfExists":{"aws:SourceAccount":"111122223333"},"Null":{"aws:SourceAccount":"false"},"Bool":{"aws:PrincipalIsAWSService":"true"}}` (written as an RCP; evaluated here as a bucket policy) | service principal with `aws:SourceAccount`=111122223333 | not denied | derived | S-KEYS `#condition-keys-sourceaccount` |
| S24 | same | service principal with `aws:SourceAccount`=444455556666 | ExplicitDeny | derived | same |
| S25 | same | IAM principal | not denied | example ("calls by your principals aren't impacted") | same |
| S26 | same | service principal without `aws:SourceAccount` | not denied | example ("service integrations that don't require the use of this key ... aren't impacted") | same |

### 9.18 Service principals and confused-deputy conditions (T)

Source S-DEPUTY `#cross-service-confused-deputy-prevention-resource`.

```
T.ct   [{"Sid":"CloudTrailAclCheck","Effect":"Allow","Principal":{"Service":"cloudtrail.amazonaws.com"},"Action":"s3:GetBucketAcl","Resource":"arn:aws:s3:::amzn-s3-demo-bucket1","Condition":{"StringEquals":{"aws:SourceAccount":"111122223333"}}}]
T.as   [{"Effect":"Allow","Principal":{"Service":["appstream.amazonaws.com"]},"Action":"s3:GetObject","Resource":"arn:aws:s3:::amzn-s3-demo-bucket2/examplefile.psh","Condition":{"ArnEquals":{"aws:SourceArn":"arn:aws:appstream:us-east-1:111122223333:fleet/ExampleFleetName"}}}]
```

| ID | Caller, context | Policies | Expected | Basis |
|---|---|---|---|---|
| T1 | cloudtrail.amazonaws.com, `aws:SourceAccount`=111122223333, `s3:GetBucketAcl` | T.ct | Allow | example |
| T2 | same with `aws:SourceAccount`=444455556666 | T.ct | ImplicitDeny | example |
| T3 | appstream.amazonaws.com, `aws:SourceArn`=the fleet, `s3:GetObject` | T.as | Allow | example |
| T4 | same with another fleet ARN | T.as | ImplicitDeny | example |
| T5 | an IAM user, `s3:GetBucketAcl` | T.ct | ImplicitDeny (the statement names only the service) | derived |

### 9.19 Third-party-recorded simulator outcomes (U)

Source: localstack@8b9a79f `tests/aws/services/iam/test_iam.snapshot.json:L6245-L6320` (`test_simulate_principle_policy[role]`, validated 2025-04-21 per `tests/aws/services/iam/test_iam.validation.json:L65-L67`; the `[group]` and `[user]` variants at `:L6323` and `:L6401` record the same decisions). Policy, from the test source `tests/aws/services/iam/test_iam.py:L461-L473`: `{"Version":"2012-10-17","Statement":{"Sid":"","Effect":"Allow","Action":"s3:PutObject","Resource":"*"}}` attached to a role, a group or a user; `SimulatePrincipalPolicy` with the bucket ARN.

| ID | Action, resource | Recorded `EvalDecision` | Basis |
|---|---|---|---|
| U1 | `s3:PutObject` `arn:aws:s3:::<bucket>` | `allowed`, one `MatchedStatements` entry (`SourcePolicyType` `IAM Policy`, `StartPosition` line 1 column 41, `EndPosition` line 1 column 113), `OrganizationsDecisionDetail.AllowedByOrganizations` true | 3p |
| U2 | `s3:GetObjectVersion` same | `implicitDeny`, no matched statements | 3p |

### 9.20 Examples left out

- S-COND `#AccessPolicyLanguage_RequestContext` MFA example: the request is a role session (`AROA...`) yet the policy's Resource uses `${aws:username}`, which a role session does not carry (7.2), and the context lists `arn:aws:iam::user/martha` without an account; only its condition (P5, P6) is transcribed (11, C6).
- S-CTX PARC context list (`aws:PrincipalARN=arn:aws:iam::AIDA123456789EXAMPLE:role/HR`, account `1123456789012`): malformed identifiers (11, C6); its table is transcribed as O1 to O4.
- S-TAGS trust policy example and the session-tag tutorials: inputs incomplete in the page text.
- EC2 `RunInstances` multi-resource example: transcribed only at condition level (J38 to J41) because the set of resources AWS authorizes for `RunInstances` is service-specific.
- S-DEPUTY RCP example and S-KEYS RCP example: RCPs do not exist in understudy; the conditions are transcribed as S23 to S26.

## 10. The AWS Managed Policy Reference Guide

### 10.1 robots.txt

ROBOTS (retrieved 2026-10-25): `User-agent: *`, a `Sitemap: https://docs.aws.amazon.com/sitemap_index.xml` line, about 100 `Disallow:` lines (legacy API versions under `/AmazonCloudFront/`, `/AWSEC2/`, `/AmazonRDS/` and others, plus `/forms/`, `/en_pv/`, `/help-panel/`, `/search/`, `/ask-docs/`), none under `/aws-managed-policy/`, and `Crawl-delay: 5`.

### 10.2 llms.txt

M-LLMS, retrieved 2026-10-25: `Content-Type: text/plain`, 331,227 bytes, `Last-Modified: Sun, 25 Oct 2026 17:54:38 GMT`, `Cache-Control: max-age=600`, an `ETag`. Layout, exactly:

```
# AWS Managed Policy Reference Guide

- [What are AWS managed policies?](https://docs.aws.amazon.com/aws-managed-policy/latest/reference/about-managed-policy-reference.md)

## [AWS managed policies](https://docs.aws.amazon.com/aws-managed-policy/latest/reference/policy-list.md)

- [<Name>](https://docs.aws.amazon.com/aws-managed-policy/latest/reference/<Name>.md): About the AWS managed policy: <Name>
  ... 1,608 such lines, sorted case-insensitively (AccessAnalyzerServiceRolePolicy first) ...

## Full content

The entire guide is also available as a single markdown file, useful for feeding the full guide to an LLM in one pass:

- [Complete guide (single file)](https://docs.aws.amazon.com/aws-managed-policy/latest/reference/full.md)
```

All 1,608 policy lines match `^- \[[^]]+\]\(https://docs.aws.amazon.com/aws-managed-policy/latest/reference/[^)]+\.md\): About the AWS managed policy: `.

### 10.3 Per-policy page

`<Name>.md` (`Content-Type: text/markdown; charset=utf-8`, `Cache-Control: max-age=300, stale-if-error=600`). AWSLambdaBasicExecutionRole and AmazonSQSFullAccess (and AmazonS3FullAccess) have this layout, line for line (two leading blank lines; trailing spaces where shown as `␠`):

~~~~
# <Name>
<a name="<Name>"></a>

**Description**: <text>

`<Name>` is an [AWS managed policy](https://docs.aws.amazon.com/IAM/latest/UserGuide/access_policies_managed-vs-inline.html#aws-managed-policies).

## Using this policy
<a name="<Name>-how-to-use"></a>

You can attach `<Name>` to your users, groups, and roles.

## Policy details
<a name="<Name>-details"></a>
+ **Type**: <type>␠
+ **Creation time**: <Month DD, YYYY, HH:MM UTC>␠
+ **Edited time:** <Month DD, YYYY, HH:MM UTC>
+ **ARN**: `arn:aws:iam::aws:policy/<path><Name>`

## Policy version
<a name="<Name>-version"></a>

**Policy version:** v<N> (default)

The policy's default version is the version that defines the permissions for the policy. ...␠

## JSON policy document
<a name="<Name>-json"></a>

```                                  (a bare fence, no language tag)
{
  "Version" : "2012-10-17",
  ...                                  (two-space indent, "key" : value spacing, AWS's key order)
}
```

## Learn more
<a name="<Name>-learn-more"></a>
+ [...]  (four fixed links)
~~~~

Observed values: AWSLambdaBasicExecutionRole: Type `Service role policy`, created and edited `April 09, 2015, 15:03 UTC`, ARN `arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole`, `v1 (default)`, document `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["logs:CreateLogGroup","logs:CreateLogStream","logs:PutLogEvents"],"Resource":"*"}]}`. AmazonSQSFullAccess: Type `AWS managed policy`, created and edited `February 06, 2015, 18:41 UTC`, ARN `arn:aws:iam::aws:policy/AmazonSQSFullAccess`, `v1 (default)`, document `{"Version":"2012-10-17","Statement":[{"Action":["sqs:*"],"Effect":"Allow","Resource":"*"}]}` (Action before Effect, as published). AmazonS3FullAccess: edited `September 27, 2021, 20:16 UTC`, `v2 (default)`, actions `s3:*`, `s3-object-lambda:*`.

Parser notes: the colon sits outside the bold for `Type`, `Creation time` and `ARN` and inside it for `Edited time:` and `Policy version:`; anchors are the stable keys; the policy document is the only fenced block on the page.

### 10.4 full.md

M-FULL: 8,326,656 bytes, `Content-Type: text/markdown; charset=utf-8`, `Last-Modified: Sun, 25 Oct 2026 17:54:17 GMT`; byte ranges are honored (`206` for `Range: bytes=0-12000`). It opens with `# AWS Managed Policy Reference Guide`, a copyright banner ("Copyright &copy; Amazon Web Services, Inc. and/or its affiliates. All rights reserved."), a trademark paragraph and a table of contents, then every page with headings demoted one level (`## <Name>`, `### Policy details`, `### JSON policy document`) and the same `<a name>` anchors. Counts on 2026-10-25: 1,608 `**Policy version:** v<N> (default)` lines, 1,608 `Creation time`, `Edited time` and `ARN` lines; highest version v190; Type and ARN path agree one for one: `AWS managed policy` 998 (no path), `Service-linked role policy` 365 (`aws-service-role/`), `Service role policy` 230 (`service-role/`), `Job function policy` 15 (`job-function/`). The about page in it has a "Deprecated AWS managed policies" section. Headings are markdown-escaped, which the three pages of 10.3 cannot show since their names hold no `_`. Counted on 2026-10-26 in the same M-FULL body (8,326,656 bytes, sha256 `c976527cd05925ed951ba7fe185f073daf6a129fb021f1312b86aaf1133ca5f8`): each of the 40 policy names holding `_` is headed with every `_` written `\_` (`## AmazonConnect\_FullAccess`, 43 escapes in all), and none of the 40 is also headed with its plain name, while its anchors, its ARN line and its llms.txt line keep the plain name; 10 `**Description**:` lines carry the same escapes (`\_`, once `\*`) and no other backslash escape.

### 10.5 Mapping to IAM's policy fields

- From the page: `PolicyName` (= `<Name>`), `Arn`, `Path` (from the ARN), `Description`, `DefaultVersionId` (`v<N>`), the default version's document, `CreateDate` and `UpdateDate` at minute precision.
- Not on the page: `PolicyId` (`ANPA...`), `AttachmentCount`, `PermissionsBoundaryUsageCount`, `IsAttachable`, any non-default version. Whether the page's Description and Edited time equal IAM's `Description` and `UpdateDate`, what seconds IAM reports, and what `IsAttachable` a service-linked role policy has, is UNVERIFIED (IAM-EVAL-32).
- The ADR 0016 amendment names the guide's `llms.txt` "and the `.md` page it lists for each policy"; `full.md` is itself listed in `llms.txt`, so one `full.md` request (cross-checked against a sample of single pages) stays inside the amendment and inside the crawl delay. Whether the copyright banner of `full.md` affects the CC BY-SA 4.0 notice the amendment plans is a question for the maintainer, not settled here.

### 10.6 The 2026-10-26 run against policydoc

The maintainer's `-fetch` of 2026-10-26 (`third_party/aws-managed-policies/SOURCE.json`) read the M-FULL body of 10.4 again (8,326,656 bytes, same sha256) and parsed 1,608 policies, 998 / 365 / 230 / 15 by Type, highest default version v190, the counts of 10.4. Every document is Version 2012-10-17 and none has a Principal or NotPrincipal. The post-write `-verify`, which then applied `policydoc.ValidateIdentity`, refused five documents, each for a rule policydoc ports from moto's CreatePolicy validator (`moto/iam/policy_validation.py@608bfa9`):

| Policy (Type, default version) | Resources | Rule broken |
|---|---|---|
| AWSCloudWatchOmniServiceRolePolicy (Service-linked role policy, v2) | `arn:aws:iam::aws:role-template/cloudwatch.amazonaws.com/CloudWatchOmniAWSIntegrationForOrgRoleTemplate:1`, for `iam:GetRoleTemplateVersion` | IAM resource path: `role-template/` is not in moto's list |
| AWSObservabilityAdminTelemetryEnablementServiceRolePolicy (Service-linked role policy, v16) | `arn:aws:iam::aws:role-template/telemetry-enablement.observabilityadmin.amazonaws.com/CloudWatchTelemetryRuleEKSAddonRoleTemplate:1`, for `iam:GetRoleTemplateVersion` | IAM resource path |
| SageMakerStudioAdminIAMDefaultExecutionPolicy (AWS managed policy, v27) | `arn:aws:s3:*:*:access-grants/default`, `.../default/location/*`, `.../default/grant/*` | S3 region: moto exempts `accesspoint/` alone |
| SageMakerStudioProjectRoleMachineLearningPolicy (AWS managed policy, v45) | the same, plus `arn:aws:s3:*:*:access-grants/default/*` | S3 region |
| SageMakerStudioUserIAMDefaultExecutionPolicy (AWS managed policy, v31) | `arn:aws:s3:*:*:access-grants/default/location/*`, `.../default/grant/*` | S3 region |

- `-verify` no longer applies those rules to the catalogue. They model what IAM checks on a customer's upload (CreatePolicy, Put*Policy; iam-sts-kms.md 3.3), and AWS publishes its managed policies without that call. AWS is the arbiter of what its own documents hold, so a published document that breaks one is a finding about the port, not the data. `-verify` requires each document to parse (`policydoc.Parse`) and to be an identity policy (`policydoc.ValidateIdentityForm`: Version 2012-10-17, no Principal or NotPrincipal), the form the evaluator reads (5.5); the rest of `ValidateIdentity` is not applied.
- The Service Authorization Reference's S3 ARN formats disagree with moto's S3 region rule. Its resource types table for Amazon S3 (https://docs.aws.amazon.com/service-authorization/latest/reference/list_s3.html, read 2026-10-26; the older name `list_amazons3.html` now redirects to the reference's index) gives a `${Region}` and an `${Account}` element to accessgrantsinstance (`access-grants/default`), accessgrant, accessgrantslocation, accesspoint, accesspointobject, job, storagelensconfiguration and storagelensgroup, and a fixed `us-west-2` and an `${Account}` to multiregionaccesspointrequestarn (`async-request/mrap/${Operation}/${Token}`); bucket and object carry neither, and multiregionaccesspoint an account but no region. The formats describe resources for authorization, not what CreatePolicy accepts. policydoc follows them: it exempts those types by the leading literal of the resource element (`access-grants/`, `accesspoint/`, `async-request/mrap/`, `job/`, `storage-lens/`, `storage-lens-group/`) when the account element is not empty, so it still refuses a bucket or object ARN with a region, an object in a bucket named `job` included (`arn:aws:s3:us-east-1::job/k`), where moto exempts `accesspoint/` alone and by prefix, an empty account included. policydoc checks no region's value, so it accepts `async-request/mrap/` in any Region. Whether CreatePolicy accepts a regional ARN of each exempted type in a customer policy, whether it holds the multi-Region access point request type to us-west-2, and whether it refuses an object in a bucket named like a type, are UNVERIFIED (IAM-EVAL-36).
- The IAM resource path list is unchanged. The Service Authorization Reference for IAM (https://docs.aws.amazon.com/service-authorization/latest/reference/list_iam.html, read 2026-10-26) lists `role-template` (`arn:${Partition}:iam::aws:role-template/${AWSServicePrincipal}/${RoleTemplateName}:${RoleTemplateMajorVersion}`), `assumed-role` (`arn:${Partition}:iam::${Account}:assumed-role/${RoleName}/${RoleSessionName}`) and `delegation-request` (`arn:${Partition}:iam::${Account}:delegation-request/${DelegationRequestId}`) beside moto's types, and does not list moto's `report/`; whether a customer CreatePolicy accepts each of the three, and still accepts `report/`, is UNVERIFIED (IAM-EVAL-35).

## 11. Conflicts between sources

- **C1.** S-OPS `#Conditions_String-wildcard`: the Result column for the Deny `ForAllValues:StringNotLike` example reads Match, No match, No match for a value inside the allowed OUs, a value outside them, and an absent key. By the documented set semantics (6.12, 6.14) the condition is false, true, true. The column reads as "request stays within the boundary", the inverse of condition truth. Vectors J14 to J16 carry the derived condition.
- **C2.** S-OPS `#conditions_arn_multivalued`: the policy JSON uses `arn:aws:cloudfront::123456789012:distribution/*`; the table prints `arn:aws::cloudfront:123456789012:...` in both columns. Vectors J33 to J37 use the JSON spelling on both sides; the outcomes are unaffected.
- **C3.** S-OPS `#Conditions_Boolean` lists policy variables as supported for Bool; S-VARS `#policy-vars-conditionelement` excludes Boolean operators. IAM-EVAL-01.
- **C4.** The single-account role flow chart's last box asks "Is there an Allow in the session policy?" with No leading to implicit deny, while S-ENF says a session with no session policy gets a default session policy and is allowed. The text is followed.
- **C5.** S-VARS says an unresolved variable "might cause the entire statement to be invalid" and the principal-table note says matching an absent key "causes the statement to be invalid"; `#policy-vars-no-value` gives per-operator results (negated operators match). IAM-EVAL-16.
- **C6.** Example request contexts contain malformed identifiers: `aws:PrincipalARN=arn:aws:iam::AIDA123456789EXAMPLE:role/HR` (S-CTX), `aws:PrincipalAccount=1123456789012` and `Resource: arn:aws:iam::user/martha` (S-COND), and S-COND claims `${aws:username}` resolves for a role session. Excluded (9.20).
- **C7.** S-NOTPRIN recommends listing the account plus the user or role ARN, yet says AWS may validate account, role and then the assumed-role session. IAM-EVAL-18.
- **C8.** S-OPS states the `/32` default for any address without a prefix, IPv6 included. IAM-EVAL-07.
- **C9.** S-ENF's session-policy bullets interleave two cases ("If the principal is a role session, then the request is Allowed. Otherwise, the request is implicitly denied") without saying which branch applies when no session policy exists for a federated user; federated users are out of scope here.
- **C10.** S-XRES says the customer specifies "the APN Partner's AWS account ID in the trust policy" but its JSON names a role ARN (`arn:aws:iam::111122223333:role/APN-user-name`). Either form is valid (5.4); no vector depends on it.
- **C11.** K-OVR `#key-policy-format` limits a key policy to 32,768 bytes; the model's `com.amazonaws.kms#PolicyType` (`CreateKey` and `PutKeyPolicy` `Policy`) has `smithy.api#length` 1 to 131,072. Validate the documented 32 KB in the service, not the shape's bound; the error text for an oversized policy is not recorded.

## 12. Implications for understudy

### 12.1 Shape of the evaluator

1. Keep `internal/policydoc` for per-document matching and extend it: every operator family of 6.3 to 6.11 with the absent-key rule (6.14) instead of "assumed to hold"; set operators (6.12); policy variables with defaults, escapes and version gating (7); incomplete-ARN completion for identity policies (5.3); principal matching that reports which form matched (session ARN, user ARN, role ARN via the session's role or unique id, account, `"*"`, `"*"` with `aws:PrincipalArn`) because 2.3 needs it.
2. Put the multi-policy decision procedure (2.2, 2.4, 4.2) in one new package that owns the decision type, for example `internal/authz`, with an import-direction row in `internal/ref/deps_test.go`. Its input is typed: the principal (`identity.Principal`), the (action, resource) pairs the operation authorizes (the access recorder already computes them, `internal/access/doc.go`), the resource owner account and kind (generic, role trust, KMS key), the context, and the policy set (identity, boundary, session, resource). Its output is a sum type: `Allow`, `ImplicitDeny{PolicyType}`, `ExplicitDeny{PolicyType, PolicyARN}`, which maps directly onto the message grammar of 2.11.
3. Run it at the pipeline slot ROADMAP item 5 names, for external calls and for internal calls under their principal modes (`AsCaller`, `AsExecutionRole`, `AsService`), so `access.Advise` becomes enforcement for deliveries. A request is allowed only if every pair is allowed (2.1).
4. Configuration, not a fidelity switch: `iam.enforce` = `off` (default, today's behavior), `report` (evaluate and journal decisions, never deny), `on`. Document it in `docs/configuration.md` and `docs/guide/`.
5. Build the context per 8.1; supply `aws:RequestTag/*`, `aws:TagKeys` and `aws:ResourceTag/*` from a per-operation table; every other service-specific key is unsupplied (8.2).
6. Ship the vectors of section 9 as table tests and the rules of 6.14 and 7 as `rapid` properties (negation duality: for any context, `StringNotEquals` equals NOT `StringEquals` when the key is present; `IfExists` equals the base operator when present and true when absent; `ForAllValues` on an empty set is true, `ForAnyValue` false).

### 12.2 Operations

| Service, operation | Mark | Coverage note (exact text) |
|---|---|---|
| sts AssumeRole | partial | with iam.enforce on, the trust policy and the caller's identity policies, permissions boundary and session policies are evaluated (iam-evaluation.md 3); MFA is accepted and not checked, so aws:MultiFactorAuthPresent is false in every session; the account root user may assume roles unless iam.assume-role.root is set to refused, though AWS always refuses it (ADR 0012) |
| iam SimulateCustomPolicy | partial | evaluated by understudy's evaluator (iam-evaluation.md 2, 5, 6 and 7), not AWS's; OrderedOrganizationPolicyInputList is rejected with InvalidInput, since understudy has no AWS Organizations; MatchedStatements, MissingContextValues and EvalDecisionDetails follow the model's shapes and are not recorded against AWS |
| iam SimulatePrincipalPolicy | partial | as SimulateCustomPolicy; the principal has no group policies, since groups are not implemented |
| iam GetContextKeysForCustomPolicy, GetContextKeysForPrincipalPolicy | partial | lists the condition keys the documents name in Condition blocks; whether AWS also lists keys used only as policy variables is not recorded (iam-evaluation.md 13.18) |
| iam PutUserPolicy, PutRolePolicy, CreatePolicy, CreatePolicyVersion | partial | the document is validated and stored; it is enforced only with iam.enforce on (iam-evaluation.md 2) |
| iam AttachUserPolicy, AttachRolePolicy, PutUserPermissionsBoundary, PutRolePermissionsBoundary | partial | the attachment is stored and enforced only with iam.enforce on; AWS managed policy ARNs must be in the shipped catalogue (iam-evaluation.md 10) |
| iam CreateRole, UpdateAssumeRolePolicy | partial | the trust policy is validated and stored and enforced on AssumeRole only with iam.enforce on; named principals are not checked for existence unless iam.policy.principal-existence is checked |
| iam GetPolicy, GetPolicyVersion, ListPolicies (AWS managed) | partial | AWS managed policies come from the shipped catalogue (iam-evaluation.md 10): CreateDate and UpdateDate have minute precision, PolicyId is derived, AttachmentCount counts this emulator's attachments, and only the default version exists, so another VersionId answers NoSuchEntity |
| kms CreateKey, PutKeyPolicy | partial | the key policy is stored verbatim; with iam.enforce on it is enforced and the lockout safety check runs (iam-evaluation.md 4.5); principals are not checked for existence unless iam.policy.principal-existence is checked; grants are not implemented |
| sqs AddPermission, SetQueueAttributes (Policy); sns AddPermission, SetTopicAttributes (Policy); s3 PutBucketPolicy; lambda AddPermission; secretsmanager PutResourcePolicy | keep current mark | append to the current note: "; enforced only with iam.enforce on (iam-evaluation.md 2.3)" |
| sts GetFederationToken, AssumeRoleWithWebIdentity, AssumeRoleWithSAML, AssumeRoot, DecodeAuthorizationMessage | stay unbound (501) | federated, OIDC and SAML principals, centralized root sessions and encoded authorization messages are not modeled (2.7, 5.4) |
| kms CreateGrant, ListGrants, RetireGrant, RevokeGrant | unbound (501) | 4.6 |
| iam group operations | unbound (501) | no group policies exist to evaluate |

Binding `SimulateCustomPolicy` also gives the conformance suite a parity harness: the same request recorded against AWS (section 13) replays against understudy.

### 12.3 Fidelity switches

| Switch | Values | Default | Label | Grounds |
|---|---|---|---|---|
| `iam.assume-role.root` | `allowed`, `refused` | `allowed` | inferred | 3.5; ADR 0012 deviation |
| `iam.context.secure-transport` | `assume-tls`, `listener` | `assume-tls` | inferred | 8.2 |
| `iam.context.source-ip` | `peer`, `absent` | `peer` | inferred | 8.2 |
| `iam.context.mfa-present` | `false-in-sessions`, `absent` | `false-in-sessions` | aws-doc | 8.1 (S-KEYS `#condition-keys-multifactorauthpresent`); IAM-EVAL-26 |
| `iam.context.service-principal-identity-keys` | `absent`, `present` | `absent` | inferred | 2.9; IAM-EVAL-33 |
| `iam.condition.unsupplied-key` | `absent`, `assume-holds` | `absent` | inferred | 8.2 |
| `iam.condition.bool-variables` | `substituted`, `literal` | `substituted` | inferred | 6.6; IAM-EVAL-01 |
| `iam.condition.multivalued-plain-operator` | `any-value`, `all-values`, `no-match` | `any-value` | inferred | 6.12; IAM-EVAL-02 |
| `iam.condition.empty-value` | `null`, `value` | `null` | inferred | 6.11; IAM-EVAL-05 |
| `iam.condition.ipv6-default-prefix` | `128`, `32` | `128` | inferred | 6.8; IAM-EVAL-07 |
| `iam.variables.unresolved` | `value-null`, `statement-void` | `value-null` | inferred | 7.3; IAM-EVAL-16 |
| `iam.not-principal.match` | `account-and-principal`, `any-listed`, `every-level` | `account-and-principal` | inferred | 5.4; IAM-EVAL-18 |
| `iam.wildcard-principal.role-session` | `direct`, `limited` | `direct` | inferred | 2.3; IAM-EVAL-19 |
| `iam.wildcard-principal.service` | `matches`, `excludes` | `matches` | inferred | 2.9; IAM-EVAL-33 |
| `iam.resource-tag.case-duplicates` | `absent`, `first` | `absent` | inferred | 6.13; IAM-EVAL-31 |
| `iam.policy.principal-existence` | `unchecked`, `checked` | `unchecked` | inferred | 3.4; IAM-EVAL-24 |
| `iam.resource-policy.account-id-principal` | `verbatim`, `root-arn` | `verbatim` | inferred | 5.4; IAM-EVAL-25 |
| `kms.key-policy.role-boundary` | `limited`, `not-limited` | `limited` | inferred | 4.3; IAM-EVAL-27 |
| `kms.key-policy.lockout-check` | `with-enforcement`, `always`, `never` | `with-enforcement` | aws-doc | 4.5 |
| `kms.cross-account.management-error` (exists) | `NotFoundException` | `NotFoundException` | inferred | 4.4; IAM-EVAL-30 |

Defaults that a recording overturns change value and label to aws-recorded; none of these changes the decision procedure itself.

### 12.4 Limits, validation and error texts

- Denials: the message grammar and `<type>` spellings of 2.11; one policy type per message. For an implicit deny it is the step of 2.2 that failed. For an explicit deny found in several policy types, which type AWS names is UNVERIFIED (IAM-EVAL-21); understudy names the first in the order identity, boundary, session, resource (inferred); `on resource: <arn>` included whenever the request has a resource ARN (inferred from S-AD's examples). Wire codes per the 2.11 table; unmodeled services per IAM-EVAL-21.
- STS: session policies' combined plaintext at most 2,048 characters, at most 10 `PolicyArns`, incomplete ARNs rejected with `MalformedPolicyDocument` (2.7); the message texts are captured by IAM-EVAL-21.
- KMS: 32,768 bytes, the character set, required elements, the lockout check and principal existence (4.5).
- Simulator: `ResourcePolicy` 1 to 131,072 characters with the documented character pattern, one resource policy per simulation; `CallerArn` "cannot specify the ARN of an assumed role, federated user, or a service principal"; "Simulation of resource-based policies isn't supported for IAM roles" (IAM-SIM). Errors `com.amazonaws.iam#InvalidInputException` (`InvalidInput`, 400), `com.amazonaws.iam#NoSuchEntityException` (principal mode), `com.amazonaws.iam#PolicyEvaluationException` (`PolicyEvaluation`, 500). Decisions are the enum `com.amazonaws.iam#PolicyEvaluationDecisionType`: `allowed`, `explicitDeny`, `implicitDeny`; context types `com.amazonaws.iam#ContextKeyTypeEnum`: `string`, `stringList`, `numeric`, `numericList`, `boolean`, `booleanList`, `ip`, `ipList`, `binary`, `binaryList`, `date`, `dateList`. Both simulate operations are `@paginated` on `Marker`/`MaxItems` over `EvaluationResults`.

### 12.5 Terraform

1. Provider bootstrap calls `sts:GetCallerIdentity`, which needs no permission (iam-sts-kms.md 4.1); never deny it.
2. Terraform runs with unminted keys, which resolve to root (ADR 0012); root's full access (2.6) keeps `apply` working under `iam.enforce: on`. A configuration whose bucket policy denies `"Principal": "*"` must still be destroyable: implement the S3 bucket-owner root bypass for `GetBucketPolicy`, `PutBucketPolicy` and `DeleteBucketPolicy` (2.6). For SQS, until IAM-EVAL-23 is recorded, do not invent a bypass; journal the denial.
3. Terraform retries on `Invalid principal in policy` (IAM, 2 min), any KMS `MalformedPolicyDocumentException` (2 min on create, 3 min on a key-policy update), Secrets Manager `This resource policy contains an unsupported principal` (2 min), S3 `MalformedPolicy` (2 min), Lambda `cannot be assumed by Lambda` (5 min) (3.4, 3.6). With `iam.policy.principal-existence: checked` a typo stalls `apply` for those windows before failing, as on AWS; journal the cause so the wait is explainable.
4. Empty second plan: enforcement must not change stored policy text; services keep returning what they stored (or their documented normalization, IAM-EVAL-25). awspolicyequivalence treats account ids and root ARNs as equal (5.4) but not unique ids and ARNs, so a trust policy shown with a deleted principal's id produces a diff, as on AWS.
5. `data "aws_iam_principal_policy_simulation"` calls `SimulatePrincipalPolicy` with `MaxItems=1000`, follows `Marker`, reads `EvalDecision` (`allowed` counts as allowed) and sets `all_allowed` only when at least one result is allowed and none denied (hashicorp/terraform-provider-aws v6.68.0 `internal/service/iam/principal_policy_simulation_data_source.go:L227-L267`, `:L293-L294`, `:L338`). It works once 12.2 binds the operation.
6. Terraform's retries match message substrings `no identity-based policy allows` and `is not authorized to perform` (tf-compute.md 8; tf-messaging.md 3.3): enforcement denials must use the 2.11 grammar verbatim.

### 12.6 Never fake

- Never treat an unevaluated condition as holding under enforcement (8.2), and never supply organization, VPC or MFA keys (8.1).
- Never evaluate an SCP or RCP that does not exist, or accept SCP inputs in the simulator (12.2).
- Never answer a grant question with yes; grant operations stay unbound (4.6).
- Never serve a made-up AWS managed policy document: an ARN outside the catalogue is `NoSuchEntity`; the current placeholder document (`docs/coverage.md` iam GetPolicyVersion) goes once the catalogue ships.
- Never decode a real-looking access key into a foreign account (ADR 0012).
- Never report `aws:MultiFactorAuthPresent` true or an `aws:MultiFactorAuthAge`: MFA is not checked.
- Never fabricate `aws:SourceIp` values; `peer` or `absent` only (8.2).

## 13. Recording queue

### 13.1 Shared setup

Run in the recording shell of `docs/recording-kit.md` 1 and 2 (`$P`, `$R`, `$A`, `$Q`, `$REC`, `rec`, `recurl` set). Agents never run these. `POL` is used for policy text so that `$P` (the prefix) is never overwritten. IAM, STS and `simulate-custom-policy` calls are free; SQS, SNS and S3 at these volumes are within the free tier (`docs/recording-kit.md` 4).

```sh
export CALLER=$(aws sts get-caller-identity --query Arn --output text); echo "$CALLER"

# sim <NN>/<step> <policy-json> <action> <resource-arn> [more simulate-custom-policy args]
sim() { local id=$1 pol=$2 act=$3 res=$4; shift 4
  rec "IAM-EVAL-$id" aws iam simulate-custom-policy --policy-input-list "$pol" \
    --action-names "$act" --resource-arns "$res" "$@"; }

# A trust policy that lets this account's principals assume a test role.
TRUST='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::'"$A"':root"},"Action":"sts:AssumeRole"}]}'

# asrole <role-name> <session-name> <rec args...>: one rec step as a new session of the role.
asrole() { local role=$1 sess=$2; shift 2; local c
  c=$(aws sts assume-role --role-arn "arn:aws:iam::$A:role/$role" --role-session-name "$sess" \
      --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text) || return
  ( unset AWS_PROFILE; read -r AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN <<<"$c"
    export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN; rec "$@" ); }

# qattr <policy-json>: the SetQueueAttributes Attributes map holding that policy.
qattr() { perl -MJSON::PP -e 'print JSON::PP->new->canonical->encode({Policy=>$ARGV[0]})' "$1"; }

# Test roles used by several items.
aws iam create-role --role-name $P-eval-none --assume-role-policy-document "$TRUST"
aws iam create-policy --policy-name $P-eval-bnd \
  --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListAllMyBuckets","Resource":"*"}]}'
aws sqs create-queue --queue-name $P-eval-star
sleep 15   # IAM propagation
```

AssumeRole refuses the root user (3.5). If `$CALLER` ends in `:root`, record IAM-EVAL-22 and IAM-EVAL-23 first, then switch the shell to an IAM user for the items that assume roles:

```sh
aws iam create-user --user-name $P-eval-admin
aws iam attach-user-policy --user-name $P-eval-admin --policy-arn arn:aws:iam::aws:policy/AdministratorAccess
read -r AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY <<<"$(aws iam create-access-key --user-name $P-eval-admin \
  --query 'AccessKey.[AccessKeyId,SecretAccessKey]' --output text)"
unset AWS_SESSION_TOKEN AWS_PROFILE; export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY; sleep 15
export CALLER=$(aws sts get-caller-identity --query Arn --output text); echo "$CALLER"
```

Shared teardown, after every item's own teardown (from the root or original login session: `aws login --profile <profile> --remote` and the kit's `eval` line):

```sh
aws sqs delete-queue --queue-url "$Q/$P-eval-star"
aws iam delete-role --role-name $P-eval-none
aws iam delete-policy --policy-arn arn:aws:iam::$A:policy/$P-eval-bnd
# only if created:
AK=$(aws iam list-access-keys --user-name $P-eval-admin --query 'AccessKeyMetadata[0].AccessKeyId' --output text)
aws iam delete-access-key --user-name $P-eval-admin --access-key-id "$AK"
aws iam detach-user-policy --user-name $P-eval-admin --policy-arn arn:aws:iam::aws:policy/AdministratorAccess
aws iam delete-user --user-name $P-eval-admin
```

Simulator results are AWS's evaluator; "Simulation results can still differ from live behavior for some advanced configurations" (S-SIM). Label simulator captures aws-recorded with the note "simulator".

### 13.2 IAM-EVAL-01: Bool with a policy variable

Settles 6.6 and C3, switch `iam.condition.bool-variables`.

```sh
POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{"Bool":{"aws:SecureTransport":"${aws:PrincipalTag/tls}"}}}]}'
sim 01/01-tag-true "$POL" s3:GetObject arn:aws:s3:::b/k --context-entries '[{"ContextKeyName":"aws:SecureTransport","ContextKeyType":"boolean","ContextKeyValues":["true"]},{"ContextKeyName":"aws:PrincipalTag/tls","ContextKeyType":"string","ContextKeyValues":["true"]}]'
sim 01/02-tag-false "$POL" s3:GetObject arn:aws:s3:::b/k --context-entries '[{"ContextKeyName":"aws:SecureTransport","ContextKeyType":"boolean","ContextKeyValues":["true"]},{"ContextKeyName":"aws:PrincipalTag/tls","ContextKeyType":"string","ContextKeyValues":["false"]}]'
```

`allowed` then `implicitDeny`: substituted. `implicitDeny` both, or `InvalidInput`: literal or unsupported. Cost USD 0, 2 minutes. Teardown: none.

### 13.3 IAM-EVAL-02: multivalued key under a plain operator

Settles 6.12, switch `iam.condition.multivalued-plain-operator`.

```sh
POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:CreateTags","Resource":"*","Condition":{"StringEquals":{"aws:TagKeys":"environment"}}}]}'
for v in '"environment"' '"environment","dept"' '"dept"'; do
  sim "02/$(echo "$v" | tr -d '",')" "$POL" ec2:CreateTags arn:aws:ec2:us-east-1:111122223333:instance/i-1 \
    --context-entries '[{"ContextKeyName":"aws:TagKeys","ContextKeyType":"stringList","ContextKeyValues":['"$v"']}]'
done
```

Allowed, allowed, denied: `any-value`; allowed, denied, denied: `all-values`; all denied: `no-match`. Cost USD 0, 2 minutes. Teardown: none.

### 13.4 IAM-EVAL-03: set operators on a single-valued key

Settles 6.12 (set of one).

```sh
for op in ForAllValues ForAnyValue; do
  POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{"'"$op"':StringEquals":{"aws:PrincipalTag/dept":"a"}}}]}'
  sim "03/$op-a" "$POL" s3:GetObject arn:aws:s3:::b/k --context-entries '[{"ContextKeyName":"aws:PrincipalTag/dept","ContextKeyType":"string","ContextKeyValues":["a"]}]'
  sim "03/$op-b" "$POL" s3:GetObject arn:aws:s3:::b/k --context-entries '[{"ContextKeyName":"aws:PrincipalTag/dept","ContextKeyType":"string","ContextKeyValues":["b"]}]'
  sim "03/$op-absent" "$POL" s3:GetObject arn:aws:s3:::b/k
done
```

Expected by the set-of-one model: ForAllValues allowed, denied, allowed; ForAnyValue allowed, denied, denied. Cost USD 0, 2 minutes. Teardown: none.

### 13.5 IAM-EVAL-04: empty values and ForAnyValue with a negated operator

Settles 6.12 and the 6.14 rows for empty values.

```sh
ANY_NE='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:CreateTags","Resource":"*","Condition":{"ForAnyValue:StringNotEquals":{"aws:TagKeys":"a"}}}]}'
ALL_EQ='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:CreateTags","Resource":"*","Condition":{"ForAllValues:StringEquals":{"aws:TagKeys":"a"}}}]}'
ANY_EQ='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:CreateTags","Resource":"*","Condition":{"ForAnyValue:StringEquals":{"aws:TagKeys":"a"}}}]}'
I=arn:aws:ec2:us-east-1:111122223333:instance/i-1
EMPTY='[{"ContextKeyName":"aws:TagKeys","ContextKeyType":"stringList","ContextKeyValues":[""]}]'
sim 04/01-any-ne-absent "$ANY_NE" ec2:CreateTags $I
sim 04/02-any-ne-empty  "$ANY_NE" ec2:CreateTags $I --context-entries "$EMPTY"
sim 04/03-any-ne-a      "$ANY_NE" ec2:CreateTags $I --context-entries '[{"ContextKeyName":"aws:TagKeys","ContextKeyType":"stringList","ContextKeyValues":["a"]}]'
sim 04/04-any-ne-b      "$ANY_NE" ec2:CreateTags $I --context-entries '[{"ContextKeyName":"aws:TagKeys","ContextKeyType":"stringList","ContextKeyValues":["b"]}]'
sim 04/05-all-eq-empty  "$ALL_EQ" ec2:CreateTags $I --context-entries "$EMPTY"
sim 04/06-any-eq-empty  "$ANY_EQ" ec2:CreateTags $I --context-entries "$EMPTY"
```

Expected (inferred): denied, allowed (the empty string is not `a`) or denied (null dataset), denied, allowed, allowed, denied. An `InvalidInput` for the empty value is itself the answer for the simulator. Cost USD 0, 3 minutes. Teardown: none.

### 13.6 IAM-EVAL-05: Null on a present empty string

Settles 6.11, switch `iam.condition.empty-value`.

```sh
for b in false true; do
  POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{"Null":{"aws:PrincipalTag/x":"'"$b"'"}}}]}'
  sim "05/null-$b" "$POL" s3:GetObject arn:aws:s3:::b/k --context-entries '[{"ContextKeyName":"aws:PrincipalTag/x","ContextKeyType":"string","ContextKeyValues":[""]}]'
done
```

`null-false` denied and `null-true` allowed: an empty value is null. The reverse: it is a value. Cost USD 0, 1 minute. Teardown: none.

### 13.7 IAM-EVAL-06: date value formats

Settles 6.5.

```sh
for v in 2020-01-01 2020-01-01T00:00:00Z 2020-01-01T00:00:00.000Z 2020-01-01T01:00:00+01:00 1577836800 1577836800000; do
  POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{"DateGreaterThan":{"aws:CurrentTime":"'"$v"'"}}}]}'
  sim "06/${v//[:+.]/_}" "$POL" s3:GetObject arn:aws:s3:::b/k \
    --context-entries '[{"ContextKeyName":"aws:CurrentTime","ContextKeyType":"date","ContextKeyValues":["2020-01-01T00:00:01Z"]}]'
done
```

Allowed: the value parsed as an instant before 00:00:01Z. Denied: parsed as later (epoch milliseconds read as seconds is year 51970) or not parsed. `InvalidInput` or `PolicyEvaluation`: rejected. Cost USD 0, 2 minutes. Teardown: none.

### 13.8 IAM-EVAL-07: addresses without a prefix length

Settles 6.8 and C8, switch `iam.condition.ipv6-default-prefix`.

```sh
sip() { POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{"IpAddress":{"aws:SourceIp":"'"$2"'"}}}]}'
  sim "07/$1" "$POL" s3:GetObject arn:aws:s3:::b/k --context-entries '[{"ContextKeyName":"aws:SourceIp","ContextKeyType":"ip","ContextKeyValues":["'"$3"'"]}]'; }
sip v6-same 2001:db8::1 2001:db8::1
sip v6-near 2001:db8::1 2001:db8::2
sip v4-near 203.0.113.5 203.0.113.6
sip v6-case 2001:DB8::1 2001:db8::1
```

`v6-near` denied: `/128`; allowed: `/32`. `v4-near` denied confirms `/32` for IPv4. Cost USD 0, 1 minute. Teardown: none.

### 13.9 IAM-EVAL-08: Bool spellings and IgnoreCase folding

Settles 6.3 and 6.6.

```sh
for v in True TRUE 1; do
  POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{"Bool":{"aws:SecureTransport":"'"$v"'"}}}]}'
  sim "08/bool-$v" "$POL" s3:GetObject arn:aws:s3:::b/k --context-entries '[{"ContextKeyName":"aws:SecureTransport","ContextKeyType":"boolean","ContextKeyValues":["true"]}]'
done
POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{"StringEqualsIgnoreCase":{"aws:PrincipalTag/n":"ÄRGER"}}}]}'
sim 08/ignorecase-nonascii "$POL" s3:GetObject arn:aws:s3:::b/k --context-entries '[{"ContextKeyName":"aws:PrincipalTag/n","ContextKeyType":"string","ContextKeyValues":["ärger"]}]'
```

Cost USD 0, 1 minute. Teardown: none.

### 13.10 IAM-EVAL-09: numeric spellings

Settles 6.4.

```sh
num() { POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListBucket","Resource":"*","Condition":{"'"$2"'":{"s3:max-keys":"'"$3"'"}}}]}'
  sim "09/$1" "$POL" s3:ListBucket arn:aws:s3:::b --context-entries '[{"ContextKeyName":"s3:max-keys","ContextKeyType":"numeric","ContextKeyValues":["'"$4"'"]}]'; }
num decimal  NumericEquals   10.0 10
num exponent NumericLessThan 1e2  99
num plus     NumericEquals   +10  10
num leading0 NumericEquals   010  10
```

Allowed: the spelling parses as that number. Cost USD 0, 1 minute. Teardown: none.

### 13.11 IAM-EVAL-10: case of resource ARNs

Settles 5.3.

```sh
POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"S3:GETOBJECT","Resource":"arn:aws:s3:::CaseBucket/Key"}]}'
sim 10/01-exact     "$POL" s3:GetObject arn:aws:s3:::CaseBucket/Key
sim 10/02-lower     "$POL" s3:GetObject arn:aws:s3:::casebucket/key
sim 10/03-arn-upper "$POL" s3:GetObject ARN:AWS:S3:::CaseBucket/Key
```

Expected: allowed (action case-insensitive, 5.2), denied, then either. Cost USD 0, 1 minute. Teardown: none.

### 13.12 IAM-EVAL-11: incomplete ARNs outside identity-policy Resource, and non-ARN values

Settles 5.3 and 6.9.

```sh
POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*","Condition":{"ArnLike":{"aws:SourceArn":"arn:aws:sns"}}}]}'
sim 11/01-arnlike-incomplete "$POL" sqs:SendMessage arn:aws:sqs:us-east-1:111122223333:q --context-entries '[{"ContextKeyName":"aws:SourceArn","ContextKeyType":"string","ContextKeyValues":["arn:aws:sns:us-east-1:111122223333:t"]}]'
POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*","Condition":{"ArnLike":{"aws:SourceArn":"*"}}}]}'
sim 11/02-arnlike-nonarn "$POL" sqs:SendMessage arn:aws:sqs:us-east-1:111122223333:q --context-entries '[{"ContextKeyName":"aws:SourceArn","ContextKeyType":"string","ContextKeyValues":["not-an-arn"]}]'
NONE='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListAllMyBuckets","Resource":"*"}]}'
RP='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::'"$A"':role/'"$P"'-eval-none"},"Action":"s3:GetObject","Resource":"arn:aws:s3"}]}'
sim 11/03-resource-policy-incomplete "$NONE" s3:GetObject arn:aws:s3:::b/k --resource-policy "$RP" \
  --caller-arn arn:aws:iam::$A:role/$P-eval-none --resource-owner arn:aws:iam::$A:root
```

If 03 answers `InvalidInput`, the simulator's note "Simulation of resource-based policies isn't supported for IAM roles" (IAM-SIM) may cover role callers: rerun 03 with an IAM user ARN (`$P-eval-admin` when 13.1 created it) as both `--caller-arn` and the policy's principal. Cost USD 0, 2 minutes. Teardown: none.

### 13.13 IAM-EVAL-12: special-character variables

Settles 7.4 and vector Q13.

```sh
for ver in 2012-10-17 2008-10-17; do
  POL='{"Version":"'"$ver"'","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/a${*}b"}]}'
  sim "12/$ver-star"    "$POL" s3:GetObject 'arn:aws:s3:::b/a*b'
  sim "12/$ver-x"       "$POL" s3:GetObject 'arn:aws:s3:::b/axb'
  sim "12/$ver-literal" "$POL" s3:GetObject 'arn:aws:s3:::b/a${*}b'
done
POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{"StringEquals":{"aws:PrincipalTag/x":"a${*}b"}}}]}'
sim 12/stringequals "$POL" s3:GetObject arn:aws:s3:::b/k --context-entries '[{"ContextKeyName":"aws:PrincipalTag/x","ContextKeyType":"string","ContextKeyValues":["a*b"]}]'
```

Cost USD 0, 2 minutes. Teardown: none.

### 13.14 IAM-EVAL-13: missing variable inside NotResource

Settles 7.3.

```sh
POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"},{"Effect":"Deny","Action":"s3:GetObject","NotResource":"arn:aws:s3:::b/${aws:username}/*"}]}'
sim 13/01-no-username "$POL" s3:GetObject arn:aws:s3:::b/alice/k
sim 13/02-username    "$POL" s3:GetObject arn:aws:s3:::b/alice/k --context-entries '[{"ContextKeyName":"aws:username","ContextKeyType":"string","ContextKeyValues":["alice"]}]'
```

`explicitDeny` then `allowed`: the entry excludes nothing when unresolved. Cost USD 0, 1 minute. Teardown: none.

### 13.15 IAM-EVAL-14: where variables may appear

Settles 7.1.

```sh
POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/${aws:TagKeys}"}]}'
sim 14/01-multivalued "$POL" s3:GetObject arn:aws:s3:::b/x --context-entries '[{"ContextKeyName":"aws:TagKeys","ContextKeyType":"stringList","ContextKeyValues":["x"]}]'
POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/${aws:nosuchkey}"}]}'
sim 14/02-unknown-literal "$POL" s3:GetObject 'arn:aws:s3:::b/${aws:nosuchkey}'
sim 14/03-unknown-value   "$POL" s3:GetObject arn:aws:s3:::b/x --context-entries '[{"ContextKeyName":"aws:nosuchkey","ContextKeyType":"string","ContextKeyValues":["x"]}]'
POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:${aws:PrincipalTag/act}","Resource":"*"}]}'
sim 14/04-in-action "$POL" s3:GetObject arn:aws:s3:::b/x --context-entries '[{"ContextKeyName":"aws:PrincipalTag/act","ContextKeyType":"string","ContextKeyValues":["GetObject"]}]'
```

Cost USD 0, 2 minutes. Teardown: none.

### 13.16 IAM-EVAL-15: default-value syntax

Settles 7.4.

```sh
for form in "\${aws:PrincipalTag/team, 'company-wide'}" "\${aws:PrincipalTag/team,'company-wide'}"; do
  POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::b-'"$form"'"}]}'
  tag=$( [ "${form#*, }" != "$form" ] && echo space || echo nospace )
  sim "15/$tag-default" "$POL" s3:ListBucket arn:aws:s3:::b-company-wide
  sim "15/$tag-tagged"  "$POL" s3:ListBucket arn:aws:s3:::b-yellow --context-entries '[{"ContextKeyName":"aws:PrincipalTag/team","ContextKeyType":"string","ContextKeyValues":["yellow"]}]'
done
```

Cost USD 0, 1 minute. Teardown: none.

### 13.17 IAM-EVAL-16: one unresolved value in a list

Settles 7.3, C5, vector J6, switch `iam.variables.unresolved`.

```sh
POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListBucket","Resource":"*","Condition":{"StringLike":{"s3:prefix":["home/","home/${aws:username}/"]}}}]}'
sim 16/01-condition "$POL" s3:ListBucket arn:aws:s3:::b --context-entries '[{"ContextKeyName":"s3:prefix","ContextKeyType":"string","ContextKeyValues":["home/"]}]'
POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":["arn:aws:s3:::b/users/${aws:username}/*","arn:aws:s3:::b/public/*"]}]}'
sim 16/02-resource "$POL" s3:GetObject arn:aws:s3:::b/public/x
```

`allowed` both: `value-null`; `implicitDeny`: `statement-void`. Cost USD 0, 1 minute. Teardown: none.

### 13.18 IAM-EVAL-17: GetContextKeysForCustomPolicy and variables

Settles the note of 12.2 for the GetContextKeys operations.

```sh
POL='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/${aws:username}/*","Condition":{"IpAddress":{"aws:SourceIp":"203.0.113.0/24"},"StringEquals":{"s3:ExistingObjectTag/team":"${aws:PrincipalTag/team}"}}}]}'
rec IAM-EVAL-17/01 aws iam get-context-keys-for-custom-policy --policy-input-list "$POL"
```

Records whether `aws:username` and `aws:PrincipalTag/team` are listed and the case AWS returns names in. Cost USD 0, 1 minute. Teardown: none.

### 13.19 IAM-EVAL-18: NotPrincipal and role sessions

Settles 5.4 and C7, switch `iam.not-principal.match`. Needs a non-root caller (13.1).

```sh
B=$P-eval-np-$A
aws s3api create-bucket --bucket $B --create-bucket-configuration LocationConstraint=$R   # drop the configuration in us-east-1
aws iam create-role --role-name $P-eval-np --assume-role-policy-document "$TRUST"
aws iam put-role-policy --role-name $P-eval-np --policy-name list --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::'"$B"'"}]}'
sleep 15
RA='"arn:aws:iam::'"$A"':role/'"$P"'-eval-np"'; AR='"arn:aws:iam::'"$A"':root"'; SA='"arn:aws:sts::'"$A"':assumed-role/'"$P"'-eval-np/np"'
np() { aws s3api put-bucket-policy --bucket $B --policy '{"Version":"2012-10-17","Statement":[{"Effect":"Deny","NotPrincipal":{"AWS":['"$2"']},"Action":"s3:ListBucket","Resource":"arn:aws:s3:::'"$B"'"}]}' \
  && sleep 10 && asrole $P-eval-np np "IAM-EVAL-18/$1" aws s3api list-objects-v2 --bucket $B; }
np a-role "$RA"; np b-role-account "$RA,$AR"; np c-session-role-account "$SA,$RA,$AR"; np d-account "$AR"; np e-session "$SA"; np f-session-account "$SA,$AR"
```

Which variants succeed decides the switch: only `b`, `c` and `f`: `account-and-principal`; `c` only: `every-level`; all six (`d` lists the caller's account): `any-listed`. Any other pattern fits none of the three values: record it as it is. Cost USD 0, 5 minutes. Teardown:

```sh
aws s3api delete-bucket-policy --bucket $B; aws s3api delete-bucket --bucket $B
aws iam delete-role-policy --role-name $P-eval-np --policy-name list   # keep the role until IAM-EVAL-21, -22 and -26 are done
```

### 13.20 IAM-EVAL-19: Principal "*" versus a role session's boundary

Settles 2.3 (the `"*"` row) and vector E16, switch `iam.wildcard-principal.role-session`. Needs a non-root caller.

```sh
aws iam create-role --role-name $P-eval-bnd --assume-role-policy-document "$TRUST" --permissions-boundary arn:aws:iam::$A:policy/$P-eval-bnd
sleep 15
QA=arn:aws:sqs:$R:$A:$P-eval-star
qp() { aws sqs set-queue-attributes --queue-url "$Q/$P-eval-star" --attributes "$(qattr '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":'"$2"',"Action":"sqs:GetQueueAttributes","Resource":"'"$QA"'"}]}')" \
  && sleep 10 && asrole $P-eval-bnd s1 "IAM-EVAL-19/$1" aws sqs get-queue-attributes --queue-url "$Q/$P-eval-star" --attribute-names QueueArn; }
qp a-star '"*"'
qp b-role '{"AWS":"arn:aws:iam::'"$A"':role/'"$P"'-eval-bnd"}'
qp c-session '{"AWS":"arn:aws:sts::'"$A"':assumed-role/'"$P"'-eval-bnd/s1"}'
```

Documented: `b` denied (boundary, no identity policy), `c` allowed. `a` allowed: `direct`; denied: `limited`. Cost USD 0, 3 minutes. Teardown: `aws sqs set-queue-attributes --queue-url "$Q/$P-eval-star" --attributes Policy=""`; `aws iam delete-role --role-name $P-eval-bnd`.

### 13.21 IAM-EVAL-20: role path in aws:PrincipalArn

Settles 3.4 and 8.1.

```sh
aws iam create-role --role-name $P-eval-path --path /eval/ --assume-role-policy-document "$TRUST"; sleep 15
QA=arn:aws:sqs:$R:$A:$P-eval-star
pa() { aws sqs set-queue-attributes --queue-url "$Q/$P-eval-star" --attributes "$(qattr '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:GetQueueAttributes","Resource":"'"$QA"'","Condition":{"ArnEquals":{"aws:PrincipalArn":"'"$2"'"}}}]}')" \
  && sleep 10 && asrole eval/$P-eval-path s1 "IAM-EVAL-20/$1" aws sqs get-queue-attributes --queue-url "$Q/$P-eval-star" --attribute-names QueueArn; }
pa with-path "arn:aws:iam::$A:role/eval/$P-eval-path"
pa no-path   "arn:aws:iam::$A:role/$P-eval-path"
```

`asrole` is given `eval/<name>` so that `--role-arn` carries the role's path. Exactly one succeeds. Cost USD 0, 2 minutes. Teardown: `aws sqs set-queue-attributes --queue-url "$Q/$P-eval-star" --attributes Policy=""`; `aws iam delete-role --role-name $P-eval-path`.

### 13.22 IAM-EVAL-21: denial wire shapes and texts

Settles 2.11 (codes, statuses, messages for every service) and the 12.4 STS validation texts.

```sh
for c in "sqs list-queues" "sns list-topics" "s3api list-buckets" "lambda list-functions" "dynamodb list-tables" \
         "kms list-keys" "secretsmanager list-secrets" "logs describe-log-groups" "events list-event-buses" \
         "cloudwatch list-metrics --namespace AWS/SQS" "iam list-roles" "sts get-caller-identity"; do
  asrole $P-eval-none none "IAM-EVAL-21/implicit-${c%% *}" aws $c
done
aws iam put-role-policy --role-name $P-eval-none --policy-name deny --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":["sqs:ListQueues","sqs:GetQueueAttributes"],"Resource":"*"}]}'; sleep 15
asrole $P-eval-none none IAM-EVAL-21/explicit-identity aws sqs list-queues
QA=arn:aws:sqs:$R:$A:$P-eval-star
aws sqs set-queue-attributes --queue-url "$Q/$P-eval-star" --attributes "$(qattr '{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"sqs:GetQueueAttributes","Resource":"'"$QA"'"}]}')"; sleep 10
rec IAM-EVAL-21/explicit-resource aws sqs get-queue-attributes --queue-url "$Q/$P-eval-star" --attribute-names QueueArn
asrole $P-eval-none none IAM-EVAL-21/explicit-both aws sqs get-queue-attributes --queue-url "$Q/$P-eval-star" --attribute-names QueueArn
aws iam create-role --role-name $P-eval-closed --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::'"$A"':role/'"$P"'-eval-np"},"Action":"sts:AssumeRole"}]}'; sleep 15
rec IAM-EVAL-21/trust-implicit aws sts assume-role --role-arn arn:aws:iam::$A:role/$P-eval-closed --role-session-name t
rec IAM-EVAL-21/session-policy-incomplete-arn aws sts assume-role --role-arn arn:aws:iam::$A:role/$P-eval-none --role-session-name t \
  --policy '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"arn:aws:s3"}]}'
```

`sts get-caller-identity` must succeed (control). `explicit-both` shows which policy type a message names when the identity policy and the resource policy both deny. Redact the `assume-role` outputs (`docs/recording-kit.md` 2). Cost USD 0, 10 minutes. Teardown: `aws iam delete-role-policy --role-name $P-eval-none --policy-name deny`; `aws sqs set-queue-attributes --queue-url "$Q/$P-eval-star" --attributes Policy=""`; `aws iam delete-role --role-name $P-eval-closed`.

### 13.23 IAM-EVAL-22: root calling AssumeRole

Settles 3.5, vector H8, switch `iam.assume-role.root`. Only when `$CALLER` ends in `:root`, before the IAM-user switch of 13.1.

```sh
aws iam create-role --role-name $P-eval-np --assume-role-policy-document "$TRUST" 2>/dev/null; sleep 15
rec IAM-EVAL-22/01 aws sts assume-role --role-arn arn:aws:iam::$A:role/$P-eval-np --role-session-name root-try
```

Records the code, status and message. Cost USD 0, 1 minute. Teardown: none (13.19 removes the role).

### 13.24 IAM-EVAL-23: root and a deny-all queue policy

Settles 2.6 (SQS bypass) and 12.5 item 2. Only with a root session.

```sh
aws sqs create-queue --queue-name $P-eval-lock
QL=arn:aws:sqs:$R:$A:$P-eval-lock
aws sqs set-queue-attributes --queue-url "$Q/$P-eval-lock" --attributes "$(qattr '{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"sqs:*","Resource":"'"$QL"'"}]}')"; sleep 10
rec IAM-EVAL-23/01-get    aws sqs get-queue-attributes --queue-url "$Q/$P-eval-lock" --attribute-names All
rec IAM-EVAL-23/02-unset  aws sqs set-queue-attributes --queue-url "$Q/$P-eval-lock" --attributes Policy=""
rec IAM-EVAL-23/03-delete aws sqs delete-queue --queue-url "$Q/$P-eval-lock"
```

If 02 and 03 fail, the queue stays locked: remove the policy with the console's root-user task (S-ROOT `#root-user-tasks`) before tearing down. Cost USD 0, 2 minutes. Teardown: the 03 step, or the console task.

### 13.25 IAM-EVAL-24: principal existence and unique-ID display

Settles 3.4 and 12.5 item 3, switch `iam.policy.principal-existence`.

```sh
rec IAM-EVAL-24/01-missing-role aws iam create-role --role-name $P-eval-t1 --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::'"$A"':role/'"$P"'-nope"},"Action":"sts:AssumeRole"}]}'
aws iam create-role --role-name $P-eval-ref --assume-role-policy-document "$TRUST"; sleep 15
rec IAM-EVAL-24/02-create aws iam create-role --role-name $P-eval-t2 --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::'"$A"':role/'"$P"'-eval-ref"},"Action":"sts:AssumeRole"}]}'
aws iam delete-role --role-name $P-eval-ref; sleep 15
rec IAM-EVAL-24/03-after-delete aws iam get-role --role-name $P-eval-t2
aws iam create-role --role-name $P-eval-ref --assume-role-policy-document "$TRUST"; sleep 15
rec IAM-EVAL-24/04-after-recreate aws iam get-role --role-name $P-eval-t2
QA=arn:aws:sqs:$R:$A:$P-eval-star
rec IAM-EVAL-24/05-sqs-missing-user aws sqs set-queue-attributes --queue-url "$Q/$P-eval-star" --attributes "$(qattr '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::'"$A"':user/'"$P"'-nope"},"Action":"sqs:GetQueueAttributes","Resource":"'"$QA"'"}]}')"
```

Expected: 01 `MalformedPolicyDocument` with `Invalid principal in policy`; 03 and 04 show an `AROA...` id in place of the ARN. Cost USD 0, 3 minutes. Teardown: `aws iam delete-role --role-name $P-eval-t2`; `aws iam delete-role --role-name $P-eval-ref`; `aws iam delete-role --role-name $P-eval-t1 2>/dev/null`; `aws sqs set-queue-attributes --queue-url "$Q/$P-eval-star" --attributes Policy=""`.

### 13.26 IAM-EVAL-25: account-id principals after a save

Settles 5.4, switch `iam.resource-policy.account-id-principal`. (Lambda `AddPermission` is in the existing backlog, `docs/research/s3-checksum-residuals-and-record-backlog.md` 2.4; KMS is step 06 of IAM-EVAL-27.)

```sh
QA=arn:aws:sqs:$R:$A:$P-eval-star
aws sqs set-queue-attributes --queue-url "$Q/$P-eval-star" --attributes "$(qattr '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"'"$A"'"},"Action":"sqs:GetQueueAttributes","Resource":"'"$QA"'"}]}')"
rec IAM-EVAL-25/01-sqs aws sqs get-queue-attributes --queue-url "$Q/$P-eval-star" --attribute-names Policy
TA=$(aws sns create-topic --name $P-eval-norm --query TopicArn --output text)
aws sns set-topic-attributes --topic-arn "$TA" --attribute-name Policy --attribute-value '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"'"$A"'"},"Action":"sns:Publish","Resource":"'"$TA"'"}]}'
rec IAM-EVAL-25/02-sns aws sns get-topic-attributes --topic-arn "$TA"
B=$P-eval-norm-$A
aws s3api create-bucket --bucket $B --create-bucket-configuration LocationConstraint=$R   # drop the configuration in us-east-1
aws s3api put-bucket-policy --bucket $B --policy '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"'"$A"'"},"Action":"s3:ListBucket","Resource":"arn:aws:s3:::'"$B"'"}]}'
rec IAM-EVAL-25/03-s3 aws s3api get-bucket-policy --bucket $B
```

Cost USD 0, 3 minutes. Teardown: `aws sqs set-queue-attributes --queue-url "$Q/$P-eval-star" --attributes Policy=""`; `aws sns delete-topic --topic-arn "$TA"`; `aws s3api delete-bucket-policy --bucket $B`; `aws s3api delete-bucket --bucket $B`.

### 13.27 IAM-EVAL-26: aws:MultiFactorAuthPresent in a session without MFA

Settles 8.1, switch `iam.context.mfa-present`. Needs the role `$P-eval-np` (13.19) and a non-root caller.

```sh
QA=arn:aws:sqs:$R:$A:$P-eval-star
mfa() { aws sqs set-queue-attributes --queue-url "$Q/$P-eval-star" --attributes "$(qattr '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:GetQueueAttributes","Resource":"'"$QA"'"},{"Effect":"Deny","Principal":"*","Action":"sqs:GetQueueAttributes","Resource":"'"$QA"'","Condition":'"$2"'}]}')" \
  && sleep 10 && asrole $P-eval-np m "IAM-EVAL-26/$1" aws sqs get-queue-attributes --queue-url "$Q/$P-eval-star" --attribute-names QueueArn; }
mfa present '{"Null":{"aws:MultiFactorAuthPresent":"false"}}'
mfa false   '{"Bool":{"aws:MultiFactorAuthPresent":"false"}}'
```

Denied in both: present and false. Cost USD 0, 2 minutes. Teardown: `aws sqs set-queue-attributes --queue-url "$Q/$P-eval-star" --attributes Policy=""`; then `aws iam delete-role --role-name $P-eval-np`.

### 13.28 IAM-EVAL-27: a KMS key policy naming a role with a boundary

Settles 4.3, switch `kms.key-policy.role-boundary`; step 06 serves IAM-EVAL-25.

```sh
K=$(aws kms create-key --description $P-eval --query KeyMetadata.KeyId --output text); KA=arn:aws:kms:$R:$A:key/$K
aws iam create-role --role-name $P-eval-kms  --assume-role-policy-document "$TRUST" --permissions-boundary arn:aws:iam::$A:policy/$P-eval-bnd
aws iam create-role --role-name $P-eval-kms2 --assume-role-policy-document "$TRUST"; sleep 15
aws kms put-key-policy --key-id $K --policy-name default --policy '{"Version":"2012-10-17","Statement":[{"Sid":"Enable IAM User Permissions","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::'"$A"':root"},"Action":"kms:*","Resource":"*"},{"Sid":"roles","Effect":"Allow","Principal":{"AWS":["arn:aws:iam::'"$A"':role/'"$P"'-eval-kms","arn:aws:iam::'"$A"':role/'"$P"'-eval-kms2"]},"Action":"kms:DescribeKey","Resource":"*"}]}'
sleep 10
asrole $P-eval-kms  k IAM-EVAL-27/01-with-boundary    aws kms describe-key --key-id $KA
asrole $P-eval-kms2 k IAM-EVAL-27/02-without-boundary aws kms describe-key --key-id $KA
aws kms put-key-policy --key-id $K --policy-name default --policy '{"Version":"2012-10-17","Statement":[{"Sid":"acct","Effect":"Allow","Principal":{"AWS":"'"$A"'"},"Action":"kms:*","Resource":"*"}]}'
rec IAM-EVAL-27/06-account-id-principal aws kms get-key-policy --key-id $K --policy-name default
```

`01` denied and `02` allowed: `limited`. Both allowed: `not-limited`. Keep the key for IAM-EVAL-28, -29 and -34. Cost: the key, at most USD 0.25 with its 7-day deletion window (USD 1 per key-month, prorated); 4 minutes. Teardown, after IAM-EVAL-34: `aws kms schedule-key-deletion --key-id $K --pending-window-in-days 7`; `aws iam delete-role --role-name $P-eval-kms`; `aws iam delete-role --role-name $P-eval-kms2`.

### 13.29 IAM-EVAL-28: KMS denial message

Settles 2.11 for KMS. Uses the key of IAM-EVAL-27.

```sh
asrole $P-eval-none none IAM-EVAL-28/01-describe aws kms describe-key --key-id $KA
asrole $P-eval-none none IAM-EVAL-28/02-encrypt  aws kms encrypt --key-id $KA --plaintext aGk=
```

Cost USD 0 beyond IAM-EVAL-27, 1 minute. Teardown: none.

### 13.30 IAM-EVAL-29: KMS lockout and principal-existence texts

Settles 4.5 and 12.4. Uses the key of IAM-EVAL-27. Both calls are expected to fail; if 01 succeeds the key is managed only by `$P-eval-kms2`, and the recovery line, run before 02, restores it. The recovery policy keeps `$P-eval-kms2` named, since that role has no IAM policy and the lockout check must pass for it.

```sh
rec IAM-EVAL-29/01-lockout aws kms put-key-policy --key-id $K --policy-name default --policy '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::'"$A"':role/'"$P"'-eval-kms2"},"Action":"kms:*","Resource":"*"}]}'
# recovery, only if 01 succeeded, before 02:
# asrole $P-eval-kms2 r IAM-EVAL-29/99-recover aws kms put-key-policy --key-id $K --policy-name default --policy '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::'"$A"':root"},"Action":"kms:*","Resource":"*"},{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::'"$A"':role/'"$P"'-eval-kms2"},"Action":"kms:*","Resource":"*"}]}'
rec IAM-EVAL-29/02-missing-principal aws kms put-key-policy --key-id $K --policy-name default --policy '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::'"$A"':root"},"Action":"kms:*","Resource":"*"},{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::'"$A"':role/'"$P"'-nope"},"Action":"kms:DescribeKey","Resource":"*"}]}'
```

Cost USD 0 beyond IAM-EVAL-27, 2 minutes. Teardown: none.

### 13.31 IAM-EVAL-30: cross-account KMS management operations

Settles 4.4 and vector I10, switch `kms.cross-account.management-error`. Needs a second throwaway account with a profile `<profile-b>` (a second free-plan project) whose caller has administrator permissions there. On free-plan projects that project's Region must be `$R`, since the key is in `$R` and its SCPs close every other Region (`docs/recording-kit.md` 1).

```sh
export AB=$(aws sts get-caller-identity --profile <profile-b> --query Account --output text); echo "second account $AB"
aws kms put-key-policy --key-id $K --policy-name default --policy '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::'"$A"':root"},"Action":"kms:*","Resource":"*"},{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::'"$AB"':root"},"Action":"kms:*","Resource":"*"}]}'
xb() { ( unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN; export AWS_PROFILE=<profile-b>; rec "IAM-EVAL-30/$1" "${@:2}" ); }
xb 01-describe   aws kms describe-key --key-id $KA
xb 02-getpolicy  aws kms get-key-policy --key-id $KA --policy-name default
xb 03-rotation   aws kms enable-key-rotation --key-id $KA
xb 04-schedule   aws kms schedule-key-deletion --key-id $KA --pending-window-in-days 30
```

Expected: 01 allowed; 02 to 04 refused with the code the switch should carry. If 04 succeeds, cancel it from account A: `aws kms cancel-key-deletion --key-id $K`. Cost USD 0 beyond IAM-EVAL-27, 5 minutes. Teardown: none (the key goes with IAM-EVAL-27).

### 13.32 IAM-EVAL-31: tag keys differing only by case

Settles 6.13, switch `iam.resource-tag.case-duplicates`.

```sh
aws sqs create-queue --queue-name $P-eval-tag --tags env=a,Env=b
rec IAM-EVAL-31/00-tags aws sqs list-queue-tags --queue-url "$Q/$P-eval-tag"
aws iam create-role --role-name $P-eval-tag --assume-role-policy-document "$TRUST"
for v in a b; do
  aws iam put-role-policy --role-name $P-eval-tag --policy-name t --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:GetQueueAttributes","Resource":"arn:aws:sqs:'"$R"':'"$A"':'"$P"'-eval-tag","Condition":{"StringEquals":{"aws:ResourceTag/env":"'"$v"'"}}}]}'
  sleep 15; asrole $P-eval-tag t "IAM-EVAL-31/value-$v" aws sqs get-queue-attributes --queue-url "$Q/$P-eval-tag" --attribute-names QueueArn
done
```

If `create-queue` rejects the second key, record that and stop. Cost USD 0, 3 minutes. Teardown: `aws sqs delete-queue --queue-url "$Q/$P-eval-tag"`; `aws iam delete-role-policy --role-name $P-eval-tag --policy-name t`; `aws iam delete-role --role-name $P-eval-tag`.

### 13.33 IAM-EVAL-32: catalogue fields against IAM

Settles 10.5. IAM output is used only to check the mapping, never shipped (ADR 0016 managed policies amendment).

```sh
for a in AmazonSQSFullAccess service-role/AWSLambdaBasicExecutionRole AmazonS3FullAccess aws-service-role/AmazonSSMServiceRolePolicy; do
  rec "IAM-EVAL-32/${a##*/}" aws iam get-policy --policy-arn arn:aws:iam::aws:policy/$a
done
rec IAM-EVAL-32/s3full-v1 aws iam get-policy-version --policy-arn arn:aws:iam::aws:policy/AmazonS3FullAccess --version-id v1
rec IAM-EVAL-32/s3full-v2 aws iam get-policy-version --policy-arn arn:aws:iam::aws:policy/AmazonS3FullAccess --version-id v2
```

Compare `CreateDate`, `UpdateDate`, `Description`, `DefaultVersionId` and the document with 10.3, and record `IsAttachable` for the service-linked role policy, which the page does not publish and understudy answers `true`. Cost USD 0, 2 minutes. Teardown: none.

### 13.34 IAM-EVAL-33: service-principal context keys and wildcard principals

Settles 2.9, switches `iam.context.service-principal-identity-keys` and `iam.wildcard-principal.service`.

```sh
aws sqs create-queue --queue-name $P-eval-sp; QS=arn:aws:sqs:$R:$A:$P-eval-sp
TS=$(aws sns create-topic --name $P-eval-sp --query TopicArn --output text)
aws sns subscribe --topic-arn "$TS" --protocol sqs --notification-endpoint "$QS"
sp() { aws sqs set-queue-attributes --queue-url "$Q/$P-eval-sp" --attributes "$(qattr '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":'"$2"',"Action":"sqs:SendMessage","Resource":"'"$QS"'","Condition":'"$3"'}]}')" \
  && sleep 10 && aws sns publish --topic-arn "$TS" --message "$1" \
  && rec "IAM-EVAL-33/$1" aws sqs receive-message --queue-url "$Q/$P-eval-sp" --wait-time-seconds 10 \
  ; H=$(perl -ne 'print $1 if /"ReceiptHandle": "([^"]+)"/' "$REC/IAM-EVAL-33/$1.out") \
  ; [ -n "$H" ] && aws sqs delete-message --queue-url "$Q/$P-eval-sp" --receipt-handle "$H"; }
SRC='"ArnEquals":{"aws:SourceArn":"'"$TS"'"}'
sp type-present    '{"Service":"sns.amazonaws.com"}' '{'"$SRC"',"Null":{"aws:PrincipalType":"false"}}'
sp type-absent     '{"Service":"sns.amazonaws.com"}' '{'"$SRC"',"Null":{"aws:PrincipalType":"true"}}'
sp account-present '{"Service":"sns.amazonaws.com"}' '{'"$SRC"',"Null":{"aws:PrincipalAccount":"false"}}'
sp userid-present  '{"Service":"sns.amazonaws.com"}' '{'"$SRC"',"Null":{"aws:userid":"false"}}'
sp star-principal  '{"AWS":"*"}'                     '{'"$SRC"'}'
```

A message arriving means the condition held; its body names the step. `sp` deletes each received message before the next step, so a late delivery from an earlier step shows up under its own body. Cost USD 0, 8 minutes. Teardown: `aws sns delete-topic --topic-arn "$TS"`; `aws sqs delete-queue --queue-url "$Q/$P-eval-sp"`.

### 13.35 IAM-EVAL-34: aws:ViaAWSService and aws:CalledVia on S3 calls to KMS

Settles 8.1 (`aws:CalledVia*`, `aws:ViaAWSService`). Uses the key of IAM-EVAL-27.

```sh
B=$P-eval-via-$A
aws s3api create-bucket --bucket $B --create-bucket-configuration LocationConstraint=$R   # drop the configuration in us-east-1
aws iam create-role --role-name $P-eval-via --assume-role-policy-document "$TRUST"
aws iam put-role-policy --role-name $P-eval-via --policy-name p --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::'"$B"'/*"},{"Effect":"Allow","Action":["kms:GenerateDataKey","kms:Decrypt"],"Resource":"'"$KA"'"}]}'
via() { aws kms put-key-policy --key-id $K --policy-name default --policy '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::'"$A"':root"},"Action":"kms:*","Resource":"*"},{"Effect":"Deny","Principal":{"AWS":"arn:aws:iam::'"$A"':role/'"$P"'-eval-via"},"Action":"kms:GenerateDataKey","Resource":"*","Condition":'"$2"'}]}' \
  && sleep 15 && asrole $P-eval-via v "IAM-EVAL-34/$1" aws s3api put-object --bucket $B --key k --body /dev/null --server-side-encryption aws:kms --ssekms-key-id $KA; }
via via-true         '{"Bool":{"aws:ViaAWSService":"true"}}'
via via-false        '{"Bool":{"aws:ViaAWSService":"false"}}'
via calledvia-s3     '{"ForAnyValue:StringEquals":{"aws:CalledVia":["s3.amazonaws.com"]}}'
via calledvia-absent '{"Null":{"aws:CalledVia":"true"}}'
```

Each Deny fires only when its condition holds, so a failed `put-object` means: `via-true`, S3's KMS call carried `aws:ViaAWSService` true; `via-false`, it carried false; `calledvia-s3`, `aws:CalledVia` held `s3.amazonaws.com`; `calledvia-absent`, the key was absent. Cost USD 0 beyond IAM-EVAL-27, 4 minutes. Teardown: `aws s3api delete-object --bucket $B --key k`; `aws s3api delete-bucket --bucket $B`; `aws iam delete-role-policy --role-name $P-eval-via --policy-name p`; `aws iam delete-role --role-name $P-eval-via`; then IAM-EVAL-27's teardown.

### 13.36 IAM-EVAL-35: a customer policy naming IAM resource types outside moto's list

Settles 10.6 (the IAM resource path rule for `role-template/`, `assumed-role/`, `delegation-request/` and `report/`).

```sh
# iampol <step> <resource>: one customer policy per step, so a refusal names its own resource.
iampol() { rec "IAM-EVAL-35/$1" aws iam create-policy --policy-name "$P-eval-iampath-$1" \
  --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:Get*","Resource":"'"$2"'"}]}'; }
iampol 01-role-template      "arn:aws:iam::aws:role-template/cloudwatch.amazonaws.com/CloudWatchOmniAWSIntegrationForOrgRoleTemplate:1"
iampol 02-assumed-role       "arn:aws:iam::$A:assumed-role/$P-eval-none/s"
iampol 03-delegation-request "arn:aws:iam::$A:delegation-request/$P-eval-none"
iampol 04-report             "arn:aws:iam::$A:report/x"
```

policydoc refuses 01 to 03 and accepts 04, as moto does. Expected, for each step: either the policy is created, so policydoc's IAM resource path list gains that type (01 to 03) or keeps `report/` (04), or `MalformedPolicyDocument` with `IAM resource path must either be "*" or start with ...`, whose list of types is recorded verbatim, so the type stays out of the list (01 to 03) or `report/` leaves it (04). Cost USD 0, 2 minutes. Teardown, for each step that created its policy:

```sh
for s in 01-role-template 02-assumed-role 03-delegation-request 04-report; do
  grep -qx 'exit=0' "$REC/IAM-EVAL-35/$s.out" && aws iam delete-policy --policy-arn "arn:aws:iam::$A:policy/$P-eval-iampath-$s"; done
```

### 13.37 IAM-EVAL-36: a customer policy naming regional S3 resource types

Settles 10.6 (the S3 region rule): one step per type policydoc exempts, and controls for what it refuses.

```sh
# s3pol <step> <resource>: one customer policy per step, so a refusal names its own resource.
s3pol() { rec "IAM-EVAL-36/$1" aws iam create-policy --policy-name "$P-eval-s3rg-$1" \
  --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"'"$2"'"}]}'; }
s3pol 01-ag-instance              "arn:aws:s3:$R:$A:access-grants/default"
s3pol 02-ag-grant                 "arn:aws:s3:$R:$A:access-grants/default/grant/*"
s3pol 03-ag-location              "arn:aws:s3:$R:$A:access-grants/default/location/*"
s3pol 04-ag-wildcards             "arn:aws:s3:*:*:access-grants/default/grant/*"
s3pol 05-accesspoint-object       "arn:aws:s3:$R:$A:accesspoint/$P-eval/object/*"
s3pol 06-job                      "arn:aws:s3:$R:$A:job/*"
s3pol 07-storage-lens             "arn:aws:s3:$R:$A:storage-lens/*"
s3pol 08-storage-lens-group       "arn:aws:s3:$R:$A:storage-lens-group/*"
s3pol 09-mrap-request             "arn:aws:s3:us-west-2:$A:async-request/mrap/*"
s3pol 10-mrap-request-elsewhere   "arn:aws:s3:eu-west-1:$A:async-request/mrap/*"
s3pol 11-object-region            "arn:aws:s3:$R:$A:$P-eval/k"
s3pol 12-bucket-named-job         "arn:aws:s3:$R::job/k"
s3pol 13-bucket-named-accesspoint "arn:aws:s3:$R::accesspoint/k"
```

Expected, as policydoc answers each today: 01 to 10 created; 11 to 13 refused with `MalformedPolicyDocument` `Resource <arn> can not contain region information.`, the resource verbatim (moto accepts 05 and 13 and refuses the rest). A refused step among 01 to 08 drops that type's exemption; 10 refused while 09 is created limits `async-request/mrap/` to us-west-2; 12 or 13 created means the exemption goes by prefix alone, without the account element. Record each refusal's text. Cost USD 0, 3 minutes. Teardown, for each step that created its policy:

```sh
for f in "$REC"/IAM-EVAL-36/*.out; do s=$(basename "$f" .out)
  grep -qx 'exit=0' "$f" && aws iam delete-policy --policy-arn "arn:aws:iam::$A:policy/$P-eval-s3rg-$s"; done
```
