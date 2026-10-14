# R15

- Date: 2026-10-14T07:05Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

- 01-disabled encrypt/decrypt/generate-data-key/re-encrypt/enable-key-rotation (400 DisabledException "<key ARN> is disabled."): matches
- 01-disabled describe-key, get-key-rotation-status (200 false), create-alias (200), put-key-policy (200): matches
- 02-schedule (200; KeyId ARN, DeletionDate now+7d, PendingDeletion, PendingWindowInDays 7): matches
- 02-pending describe-key, get-key-rotation-status (200 false), put-key-policy (200): matches
- 02-pending encrypt/decrypt/generate-data-key/re-encrypt/enable-key-rotation/create-alias, 02-schedule-again, 02-enable-key code (400 KMSInvalidStateException): matches
- 02-pending/02-schedule-again/02-enable-key message: differs: AWS "arn:aws:kms:us-east-2:123456789012:key/<id> is pending deletion."; understudy "<ARN> is pending deletion (or pending replica deletion)." (internal/service/kms/errors.go:85)
- 02-cancel (200 {KeyId ARN}), 02-describe-after-cancel (KeyState Disabled): matches
- 02-window-6 / 02-window-31 code (400 ValidationException): matches
- 02-window-6 / 02-window-31 message: differs: AWS "PendingWindowInDays must be between 7 and 30"; understudy generated range text "1 validation error detected: Value '6' at 'pendingWindowInDays' failed to satisfy constraint: Member must have value greater than or equal to 7" (codegen/overlays/kms.json:76)
- 03-encrypt-empty, 03-encrypt-4097, 03-gdk-both, 03-gdk-neither: matches
- 03-decrypt-garbage (400 InvalidCiphertextException, no message), 03-decrypt-wrong-key-id (IncorrectKeyException): matches
- 03-create-alias-target-alias ("Aliases must refer to keys. Not aliases"), 03-create-alias-duplicate (400 AlreadyExistsException text): matches
- 03-create-alias-aws-prefix: differs: AWS 400 {"__type":"NotAuthorizedException","Message":"The alias name for a customer managed CMK cannot begin with alias/aws/."}; understudy InvalidAliasNameException, member "message", same text (internal/service/kms/errors.go:46-47)
- 03-put-key-policy-too-long: differs: AWS 400 LimitExceededException "Keys support only policies up to 32768 characters long in this region. The supplied policy was 33122 characters long." (n excludes whitespace; raw length 33136); understudy own text (errors.go:68-71; length counted with whitespace at keys.go:116)
- 03-put-key-policy-name: differs: AWS 400 ValidationException "Invalid policy name"; understudy 400 NotFoundException "No such policy exists" (errors.go:145-147, policies.go:25-30)
- 03-list-key-policies, 03-get-key-policy (byte-identical but account), 03-describe-key-missing, 03-describe-key-no-description: matches
- x-listkeys-garbage-marker: differs: AWS 400 body exactly {"__type":"InvalidMarkerException"}; understudy adds a "message" (errors.go:74-75)
- 04-disabled-key, 04-default-disabled-key-put, 04-default-disabled-key-multipart (400 KMS.DisabledException "<key ARN> is disabled."): matches
- 04-nonexistent-key (400 KMS.NotFoundException "Key '<arn>' does not exist"): matches (understudy XML-escapes the apostrophe as &#39;; decoded text equal)
- 04-nonexistent-alias (400 KMS.NotFoundException "Alias arn:aws:kms:us-east-2:123456789012:alias/<name> is not found."): matches
- 04 pending-deletion key / other-region key: n/a (not captured)
- x-ssec-on-non-ssec-read (400 InvalidRequest "The encryption parameters are not applicable to this object."): matches
- x-kms-key-id-without-aws-kms code/message/ArgumentName: matches
- x-kms-key-id-without-aws-kms ArgumentValue: differs: AWS sends none; understudy adds <ArgumentValue>aws:kms</ArgumentValue> (internal/service/s3/sse.go:116-118)
- 05-create-disabled-kms: differs: AWS 400 InvalidParameterException "The operation failed because the specified KMS key is disabled."; understudy 400 EncryptionFailure "Secrets Manager can't encrypt the secret value with the KMS key: DisabledException: ..." (internal/service/secretsmanager/errors.go:140-141 via values.go:214)
- 05-create / 05-create-duplicate (ResourceExistsException text): matches
- 05-put-token-1 / -same-value / -different-value: matches
- 05-restore-live response (200 {ARN, Name}): matches
- 05-restore-live LastChangedDate: differs: AWS advances it (visible in 05-describe-before-delete); understudy unchanged (internal/service/secretsmanager/secrets.go:358-359 tx.Unchanged)
- 05-delete-window-6 / -31 / -0 (400 InvalidParameterException "The RecoveryWindowInDays value must be between 7 and 30 days (inclusive)."): matches
- 05-delete ({ARN, Name, DeletionDate now+7d}): matches
- 05-describe-after-delete DeletedDate: differs: AWS = time of the DeleteSecret call; understudy = scheduled DeletionDate (secrets.go:416-420)
- 05-describe-after-delete LastChangedDate: differs: AWS advances it on DeleteSecret; understudy unchanged (secrets.go:301-303)
- 05-delete-twice: differs: AWS 200 {ARN, Name, DeletionDate} re-scheduled to second call + 7d; understudy 400 InvalidRequestException "You can't perform this operation on the secret because it was marked for deletion." (secrets.go:297-298)
- 05-get-scheduled (400 InvalidRequestException text): matches
- 05-restore ({ARN, Name}): matches
- 05-describe-after-restore LastChangedDate: differs: AWS advances it on RestoreSecret; understudy keeps the old value (secrets.go:362-364); DeletedDate dropped: matches
- 05-name-bang: differs: AWS 400 InvalidParameterException {"Message":"You provided an invalid name for the secret."}; understudy 400 ValidationException "Invalid name. Must be a valid name containing alphanumeric characters, or any of the following: -/_+=.@!" (errors.go:47-48)
- 05-name-513: differs: AWS 400 ValidationException "Invalid name. Must be a valid name containing alphanumeric characters, or any of the following: -/_+=.@!"; understudy generated "1 validation error detected: ... Member must have length less than or equal to 512"
- 05-arn-suffixes (6 chars [A-Za-z0-9]): matches
- 05-stage-* VersionIdsToStages trace: matches
- 06-create-role-invalid-trust: differs: AWS 400 MalformedPolicyDocument "Missing required field Principal"; understudy "Policy statement must contain actions." (internal/policydoc/validate.go:70-71)
- 06-put-role-policy-no-resource, 06-put-role-policy-duplicate-sid, 06-tag-role-case-duplicate: matches
- 06-tag-role-51: differs: AWS 400 ValidationError "1 validation error detected: Value at 'tags' failed to satisfy constraint: Member must have length less than or equal to 50"; understudy echoes "Value '[, , , ...]' at 'tags'" (internal/api/iamapi/validate_gen.go:647, internal/codegen/emit_validate.go:552-554)
- 06-delete-role-attached / -in-instance-profile / 06-delete-user-with-key (409 DeleteConflict): matches
- 06-create-policy-version-6 (409 LimitExceeded): matches
- 06-create-user / 06-create-access-key CreateDate: differs: AWS whole seconds; understudy milliseconds (internal/service/iam/view.go:81, accesskeys.go:77)
- 06-get-access-key-info-deleted: differs: AWS 200 {Account: 123456789012}; understudy 501 NotImplemented (no sts.GetAccessKeyInfo handler)
- 06-trust-lowercase / 06-trust-star (both rejected): differs (text): AWS 400 MalformedPolicyDocument "AssumeRole policy may only specify STS AssumeRole actions."; understudy "Trust Policy statement actions can only be sts:AssumeRole, sts:AssumeRoleWithSAML,  and sts:AssumeRoleWithWebIdentity" (validate.go:92-94)
- 06-assume-denied / 06-assume-ok / x-assume-11-policy-arns / x-assume-refused-session-policy: n/a (never sent; CLI refused `--role-session-name s`, min length 2, exit 252; re-record)


### Re-recorded 2026-10-14 (session name `rec`; the first run used `s`, which the CLI refuses)
- 06-assume-denied: matches (403 AccessDenied "User: <caller arn> is not authorized to perform: sts:AssumeRole on resource: <role arn>"; internal/service/sts/errors.go:27)
- 06-assume-ok and 06-get-caller-identity-assumed: matches (Arn `arn:aws:sts::123456789012:assumed-role/<role>/rec`)
- x-assume-11-policy-arns: differs: AWS 400 `ValidationError` "The limit of ARNs for the PolicyArns parameter was exceeded. 10 ARNs can be requested at most in an API request."; understudy renders a Coral constraint text (internal/service/sts/errors.go:45-48)
- x-assume-refused-session-policy: differs: AWS 400 `MalformedPolicyDocument` "Missing required field Action"; understudy says "Policy statement must contain actions." (internal/policydoc/validate.go:71)
