// Package secretsmanager implements AWS Secrets Manager: secrets, versions and
// staging labels, ClientRequestToken idempotency, tags, resource policies
// (stored, not enforced) and deletion with a recovery window on the emulator
// clock.
//
// A secret's versions are sealed with envelope encryption, as AWS does
// (iam-sts-kms.md 5.8): GenerateDataKey for an AES-256 data key under the
// secret's KMS key (the AWS managed key alias/aws/secretsmanager when it has
// none), AES-256-GCM here, and Decrypt of the stored data key to read. The
// KMS calls go through kernel.Invoke as forward access (AsCaller, Via
// KMSForward), before any cell is held, so they are journaled, faultable and
// recorded against the caller, and a disabled or deleted key surfaces as
// EncryptionFailure or DecryptionFailure. An idempotent retry decrypts the
// stored version to compare values, as AWS does; no hash of a secret value
// is ever stored.
//
// Decisions where neither the architecture nor an AWS recording settles the
// behavior:
//
//   - The KMS operations are GenerateDataKey and Decrypt, AWS's documented
//     envelope scheme, rather than Encrypt and Decrypt of the value: Encrypt
//     takes at most 4096 bytes and a secret value up to 65536.
//   - The six-character ARN suffix is derived from the request id of the
//     CreateSecret call (SHA-256, mapped onto [A-Za-z0-9]). Request ids come
//     from the seeded identifier source, so a seeded run is deterministic
//     without a new identifier stream, and a recreated secret gets another
//     suffix.
//   - The ClientRequestToken is the version id. For PutSecretValue a token
//     already used for a version is idempotent with the same value and the
//     token conflict (ResourceExistsException) otherwise; for UpdateSecret
//     any reuse is the conflict, as its API reference says. A CreateSecret
//     retry is recognized by the token the secret was created with, also
//     when it races the call it retries.
//   - Setting or changing a secret's customer KMS key without a value to
//     seal checks access to the key first (GenerateDataKey and Decrypt with
//     SecretVersionId RequestToValidateKeyAccess), so an unusable key fails
//     there with EncryptionFailure rather than at the first put.
//   - A default PutSecretValue (and UpdateSecret with a value) takes
//     AWSPENDING off the version it demotes, the one AWS-recorded trace of
//     that case; an explicit VersionStages list moves exactly those labels.
//   - Versions without labels are removed only when a secret exceeds 100
//     versions, oldest first and only once a day old; when none can go, the
//     put is LimitExceededException.
//   - LastAccessedDate is the UTC day of the last GetSecretValue.
//   - ListSecrets combines filters with AND and a filter's values with OR;
//     the primary-region and owning-service filters match no secret, since
//     understudy has neither replication nor service-owned secrets. The all
//     filter splits the value and every attribute into words (at
//     punctuation, letter-digit and case changes) and matches when any
//     value word prefixes an attribute word, ignoring case.
//   - A NextToken carries its listing and sort order; a token of another
//     listing or order is InvalidNextTokenException.
//   - More than 50 tags on a secret is InvalidParameterException; AWS
//     documents the quota but not its error.
//   - A forced DeleteSecret of a secret that does not exist answers an ARN:
//     the full ARN given, or the one a secret created under the name by
//     that call would have.
//   - RestoreSecret of a secret that is not scheduled for deletion succeeds
//     and changes nothing.
//   - UpdateSecret with a new KmsKeyId moves the data keys of the versions
//     labeled AWSCURRENT, AWSPREVIOUS and AWSPENDING under the new key (KMS
//     Decrypt, then Encrypt), as AWS does for a caller with kms:Encrypt on
//     it, which every caller has while IAM is not enforced. A version KMS
//     refuses to move (the old key disabled, deleted or otherwise refusing
//     Decrypt, or the new key refusing Encrypt) stays under its old key
//     while the key still changes, as AWS documents for a caller without
//     kms:Decrypt on the previous key or kms:Encrypt on the new one; only
//     the access check of the new key fails the update. understudy treats
//     every KMS refusal that way, a throttling fault included, since AWS
//     documents no other outcome. Versions without labels stay under the
//     key that sealed them.
//   - A KmsKeyId naming the AWS managed key aws/secretsmanager by its alias
//     ARN, key id or key ARN is stored as the default key, as the bare
//     alias is. A key id or key ARN is recognized by a KMS ListAliases run
//     as Secrets Manager itself (AsPlatform, so not recorded against the
//     caller): AWS documents no kms:ListAliases or kms:DescribeKey
//     permission for Secrets Manager callers. An alias is recognized by
//     name, since no customer alias can point at an AWS managed key.
//   - Request members understudy does not implement (AddReplicaRegions)
//     answer NotImplemented rather than being ignored; RotationToken is
//     accepted and ignored.
package secretsmanager
