# EBB notes

- Date: 2026-11-03 (16:35-16:40 UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1 (not used by this item)
- Recipe: docs/research/eventbridge-buses.md section 12. Session changes: the EBB-02 optional 101st-bus quota loop was not run (filling the account's 100-bus quota would break concurrent recordings in the same account; its teardown deletes by prefix); `>/dev/null` on two `create-role` calls and `schedule-key-deletion`; scripts ran from `$REC/_work/EBB`.

## EBB-01 (3.5, events.delete-bus.with-rules)

- delete with a rule: differs: 400 `ValidationException` "EventBus can't be deleted since it has rules." (`01-delete-with-rule`); the switch default `rejected` with code `ValidationException` matches, the text was unknown and is now recorded
- describe after the refused delete: matches (bus still present, `01-describe-after`; rule still listed, `01-rules-after`)
- delete with a rule that has a target: same 400 and text (`01-delete-with-target`)

## EBB-02 (2.2, implication 9)

- name `default`: differs: 400 `ResourceAlreadyExistsException` "Event bus default already exists." (not a name refusal)
- name with `/`: 400 `ValidationException` "Event bus name must not contain '/'." (text was UNVERIFIED; now recorded)
- name `aws.<p>`: 400 `ValidationException` "EventBus name starting with 'aws.' is not valid." (refusal not in the doc at all)
- name `aws.partner/<p>/x`: 400 `ValidationException` "EventBus name starting with 'aws.' is not valid." (the `aws.` check runs before the `/` check)
- 101st bus (`LimitExceededException` text): n/a (optional quota loop skipped: shared account with concurrent recordings)
- teardown note: `DeleteEventBus` of `aws.<p>` answers 400 `ValidationException` with the same `aws.` text, so the delete validates names; `DeleteEventBus` of `<p>/x` and `aws.partner/<p>/x` (never created) answered 200

## EBB-03 (5.4, implication 7)

- schedule-expression rule on a missing custom bus (name and ARN): differs: 400 `ResourceNotFoundException` "Event bus us1103-missing does not exist." The bus lookup runs before the schedule check, contrary to implication 7 and the R14/04 inference in 5.3 (R14's bus probably existed, or the order differs by input)
- put-rule, describe-rule, list-rules, delete-rule, enable-rule, put-targets, list-targets-by-rule, remove-targets on a missing bus: matches understudy's "Event bus <bus> does not exist." (`ResourceNotFoundException`, 400) for all eight; note `DeleteRule` on a missing bus is not idempotent here
- describe-rule on an existing bus, missing rule: matches "Rule us1103-m does not exist on EventBus us1103-e."

## EBB-04 (3.4, events.update-bus.omitted-members)

- update with name only: matches `kept` (Description, DeadLetterConfig, LogConfig unchanged in `04-describe-1`); response carries `Arn, DeadLetterConfig, Description, LogConfig, Name`
- `Description: ""`: clears it; describe omits `Description` (not `""`)
- LogConfig `NONE`/`OFF`: echoed as sent (matches 8.1)
- update of a missing bus: 400 `ResourceNotFoundException` "Event bus us1103-missing does not exist."
- update with no name: updates the default bus (response `{"Arn":...default,"Description":"us1103-default","Name":"default"}`), matches 3.6's "plausible"
- describe default afterwards: carries `CreationTime`, `Description`, `LastModifiedTime`

## EBB-05 (3.1, events.bus.kms-key-check)

- key ARN under the default key policy: differs from switch default `none`: 400 `AccessDeniedException` with a capital-M `Message` key: "Access to call kms:GenerateDataKey is denied. Reason: events.amazonaws.com is not authorized to perform: kms:GenerateDataKey on arn:aws:kms:us-east-2:<account>:key/<id> because the resource does not exist in this Region, no resource-based policies allow access, or a resource-based policy explicitly denies access"
- key id: accepted; differs: echoed (create and describe) as the key ARN, not as sent
- alias name `alias/<p>-ebb`: accepted; echoed as the alias ARN `arn:aws:kms:us-east-2:<account>:alias/us1103-ebb` (not resolved to the key)
- alias ARN: accepted, echoed as sent
- missing key ARN: differs from `none`: 400 `ValidationException` "Parameter Key 'arn:aws:kms:us-east-2:<account>:key/00000000-0000-0000-0000-000000000000' does not exist (Service: Kms, Status Code: 400, Request ID: <uuid>) (SDK Attempt Count: 1) is not valid." So AWS checks existence and the events grant: switch value `exists` (plus grant check)
- `KmsKeyIdentifier: ""` on update: clears it; describe omits `KmsKeyIdentifier` (back to the AWS owned key)

## EBB-06 (4.1, 4.3, events.bus-policy.*)

- RemoveAllPermissions on a bus with no policy: differs: 200 empty body (switch default `ResourceNotFoundException`; AWS = `succeed`)
- put s1 with a 12-digit principal: matches 4.2 (`{"AWS":"arn:aws:iam::<account>:root"}`, member order Sid, Effect, Principal, Action, Resource, no `Id`)
- put s1 again with `*`: matches (one s1 statement, Principal `"*"`)
- remove the last statement by id: matches `policy-removed` (describe has no `Policy` key)
- Action `events:PutRule`: 400 `ValidationException` "Provided value in parameter 'action' is not supported."
- Condition with a 12-digit principal: 400 `ValidationException` "Value of the parameter 'principal' must be '*' when the parameter 'condition' is set."
- Condition with `*`: accepted; rendered `"Condition":{"StringEquals":{"aws:PrincipalOrgID":"o-aaaaaaaaaa"}}` after Resource (matches the Terraform form in 4.2)
- Policy together with StatementId: 400 `ValidationException` "Action, Principal, StatementId and Condition could not be combined with Policy parameter."
- 14,588-character policy: 400 `PolicyLengthExceededException` "Policy size would be larger than the maximum allowed."

## EBB-07 (3.2, 3.3, 3.6, 5.2, events.list-buses.order, events.default-bus.creation-time)

- describe default: `{Arn, CreationTime, LastModifiedTime, Name}`; same by ARN
- default-bus CreationTime: matches `first-use`: 1793723757.834 = 2026-11-03T16:35:57.8Z, the instant of this item's `04-update-no-name` (first write to the default bus in this region); LastModifiedTime = the teardown update one second later. (No earlier write to this account's default bus is known; another session could have written it first.)
- list order: matches `by-name` (default, us1103-la, -lb, -lc, while creation order was b, c, a); default sorts first, which name order also gives
- NextToken: opaque base64 (`MQAAAG...`), `--limit 2` then the rest: matches 3.3
- describe by ARN: `Name` is the bus name
- put-rule by bus ARN: rule ARN `rule/us1103-la/us1103-lr` (matches 5.2); describe-rule and list-rules `EventBusName` is the name, not the ARN; `CreatedBy` is the account id

## EBB-08 (6.2, 6.5, implications 12-14, events.bus-target.*)

- same-account event id: matches `kept` (08-put-h1 EventId = id received in q2 via h2)
- forwarded time and resources: matches `kept` (`time` 2026-01-01T00:00:00Z and `resources ["arn:aws:s3:::rec"]` as sent)
- one hop: matches (h1 -> h2 -> h3: nothing reached q3, `08-recv-q3` `{}`); direct put to h2 reached q3 (`08-recv-q3-direct`, same id as its put)
- Input on a same-account bus target: differs from 10.1 (understudy: entry fails NotImplemented): 400 `ValidationException` "Modifying the input for target h2i is not supported." (whole call refused)
- same-account bus target without RoleArn: differs from 6.2 (RoleArn only "recommended" for same-account): 400 `ValidationException` "RoleArn is required for target arn:aws:events:us-east-2:<account>:event-bus/us1103-h2."
- bus target naming a missing bus: matches `events.bus-target.missing-bus` `dropped` at PutTargets (200, `FailedEntryCount` 0); delivery to it not observed

## EBB-09: n/a (needs a second account)

## EBB-10: n/a (needs another region; free-plan account)

## EBB-11, EBB-12: not recorded (marked superseded by scheduler.md in the recipe)

## Sweep (12.14)

All lists empty; default bus `[Description, Policy]` = `[null, null]`. KMS key PendingDeletion until 2026-11-10.
