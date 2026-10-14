# R09

- Date: 2026-10-14T07:02Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Compared against understudy built from /home/austin/dev/local-aws-stack on 2026-10-14, replaying a copy of r09.py and the r09.sh kit steps through scripts/sealed (account 123456789012). Paths are repo-relative.

- T01 (row 1, bare 100 vs Number 100): matches (delivered)
- T02 (row 2, numeric = 100 vs Number 100.0): matches (delivered)
- T03 (row 3, bare 100 vs String "100"): matches (not delivered)
- T04, T05 (rows 4-5, body string vs number): matches (not delivered)
- T06 (row 6, exists:false, no attributes): differs: AWS not delivered; understudy delivered (EmptyInputMatchable for both scopes, internal/service/sns/filterpolicy/policy.go:50-55)
- T07 (row 7, anything-but suffix): matches (accepted, delivered)
- T08 (row 8, anything-but over String.Array): matches (delivered, any-element)
- T09 (row 9, equals-ignore-case): matches (delivered)
- T10 (row 10): matches (delivered)
- T11 (row 11, 6 leaf keys in 2 nested objects): matches (rejected "FilterPolicy: Filter policy can not have more than 5 keys")
- T12 (row 12, $or branches a+c,d,e,f / b+c,d,e,f): differs: AWS rejects "Invalid parameter: Attributes Reason: FilterPolicy: Filter policy can not have more than 5 keys"; understudy accepts (keys counted per $or branch, internal/ruler/profile.go:309-317, 350-354)
- T13 (row 13, 4 wildcards in one pattern): differs: AWS "Invalid parameter: Attributes Reason: Wildcard complexity too high: pattern exceeds maximum allowed wildcards" (no "FilterPolicy:" segment); understudy "Invalid parameter: Attributes Reason: FilterPolicy: Filter policy is too complex" (internal/ruler/profile.go:361-363, 379-384). Check order not exercised (only one limit violated)
- T14 (row 14, siblings across array elements): matches (not delivered)
- T15 (row 15, numeric > 1e9 vs 2e9): matches (accepted, delivered)
- T16 (row 16, policy over 256 KB): differs: AWS "Invalid parameter: Attributes Reason: FilterPolicy: must be at most 262144 bytes long in UTF-8 encoding"; understudy "...FilterPolicy: Filter policy can not be larger than 256 KB" (internal/ruler/profile.go:254-255)
- T17 (row 17, dotted attribute name): matches (delivered)
- T18 (row 18, array root and plain body): matches (neither delivered)
- T19a, T19b (row 19, "100" and anything-but "100" vs Number 100): matches (not delivered, delivered)
- T20 nested policy under attribute scope, Subscribe and SetSubscriptionAttributes: matches ("Invalid parameter: Attributes Reason: Filter policy scope MessageAttributes does not support nested filter policy"; Set form without "Attributes Reason: ")
- H1-attr (exists:false): differs: m1 (no attributes) AWS not delivered, understudy delivered (as T06); m2 matches
- H1-body (exists:false): matches (body {} delivered, {"other":"x"} delivered); the AWS no-match rule is attribute-scope only
- H2-body (exists:true): differs: m1 {"store":null} AWS not delivered, understudy delivered (ExistsTrueAnyValue, internal/ruler/options.go:23-31); m2 "" delivered, m3 [] and m4 {} not, m5 0 and m6 false delivered all match
- H2-attr (exists:true): matches (m1 delivered; m2 empty String attribute refused at Publish with the same ParameterValueInvalid text)
- H3-b (5 leaf keys under one object): matches (accepted)
- H3-c, H3-d ($or, 6 distinct leaf keys, at most 3 or 4 per branch): differs: AWS rejects "FilterPolicy: Filter policy can not have more than 5 keys"; understudy accepts (as T12)
- H4-attr, H4-body (anything-but over arrays): matches (match, no, no, match; match, no, no)
- H5-01 to H5-06, H5-09 (numeric range and precision): matches (accept and every delivery outcome)
- H5-07 (= 9007199254740993): matches on the first line ("FilterPolicy: Cannot compare number : 9007199254740993"); location line differs (see Jackson location)
- H5-08 (= 1e309): differs: AWS "FilterPolicy: Value of equals must be numeric" with location quoting the re-serialized policy {"n":[{"numeric":["=","Infinity"]}]}; understudy "FilterPolicy: Cannot compare number : 1e309" (internal/ruler/number.go:82)
- H6-attr, H6-body, H6-numeric-string-attr (bare number): matches (Number 100, 100.0, 1.0e2 delivered; String "100" not; body 100 and 100.0 delivered, "100" not; numeric rule vs String not)
- H7b-01 anything-but suffix, H7b-02 anything-but wildcard, H7b-06 anything-but prefix list: matches (accepted)
- H7b-03 anything-but equals-ignore-case, H7b-04 prefix equals-ignore-case, H7b-05 suffix equals-ignore-case: differs: AWS accepts; understudy rejects ("Unsupported anything-but pattern: equals-ignore-case", "prefix match pattern must be a string", "suffix match pattern must be a string"; internal/ruler/profile.go:222-232)
- H7b-07 exactly: differs: AWS accepts; understudy "FilterPolicy: Unrecognized match type exactly" (internal/ruler/compile.go:609-625, profile.go:132-136)
- H7b-08 anything-but prefix false: differs: AWS "FilterPolicy: Value of anything-but must be an array or single string/number value."; understudy "FilterPolicy: prefix match pattern must be a string" (SNS special case, internal/ruler/anythingbut.go:84-90)
- H8-01 to H8-03 (body string vs number): matches (none delivered)
- H9-01, H9-02 (/32, /128): matches ("IPv4 mask bits must be < 32", "IPv6 mask bits must be < 128", no location line in AWS either)
- H9-03 (0.0.0.0/0): differs: AWS delivers the IPv6 value 2001:db8::1 in both scopes; understudy does not (netip.Prefix.Contains is per family, internal/ruler/matcher.go:259)
- H10-01 to H10-06 (malformed $or): matches on the first line (all rejected with Ruler's texts)
- Jackson location line: differs: AWS appends "\n at [Source: (String)\"<policy>\"; line: 1, column: N]" to Ruler parse-path errors (H10-*, H5-07, H5-08, H7b-08); understudy never does (internal/ruler/doc.go:91-93). Limit errors and the cidr mask errors carry none in AWS
- H11-10-single (100 points), H11-3-wildcards: matches (accepted)
- H11-11-single (121 points), H11-5-two-wildcard (5 x "*x*", rejected so 3 points per star): differs: AWS "Invalid parameter: Attributes Reason: Wildcard complexity too high"; understudy "...Reason: FilterPolicy: Filter policy is too complex" (internal/ruler/profile.go:367-369, 379-384)
- H11-4-wildcards: differs: text as T13
- limit-201: differs: status 403 and code FilterPolicyLimitExceeded match; AWS message "Could not complete request: filter policy quota exceeded"; understudy "Filter policy limit exceeded: the topic already has 200 subscriptions ..." (internal/service/sns/subscription_attrs.go:464-469)
- redrive-other-region, redrive-other-account: n/a (the kit passed two newline-joined subscription ARNs, since the CLI applied --query per page; AWS answered 400 "Invalid parameter: SubscriptionId", as understudy does for that input; the cross-account and cross-region RedrivePolicy texts remain unrecorded. Re-record with --max-items 1)
- ErrorResponse envelope: differs: AWS declares xmlns="http://sns.amazonaws.com/doc/2010-03-31/" on ErrorResponse; understudy none (internal/protocol/awsquery/codec.go:217-233)


### Re-recorded 2026-10-14: RedrivePolicy on one subscription (the first run passed two ARNs)
- redrive-other-region: AWS 400 InvalidParameter "Invalid parameter: RedrivePolicy: deadLetterTargetArn must be in the same region as the subscription"; understudy now matches (internal/service/sns/subscription_attrs.go checkRedrivePolicy; was an inferred combined text)
- redrive-other-account: AWS 400 InvalidParameter "Invalid parameter: RedrivePolicy: deadLetterTargetArn must belong to the same account as the subscription"; understudy now matches
