// Package kms implements AWS KMS for symmetric encryption keys: keys,
// aliases, key policies (stored, not enforced), rotation status, tags,
// scheduled deletion on the emulator clock, and Encrypt, Decrypt, ReEncrypt,
// GenerateDataKey and GenerateDataKeyWithoutPlaintext with real AES-256-GCM.
// Key material always comes from crypto/rand, even in a seeded run.
//
// Every operation follows the key-state table (iam-sts-kms.md 5.6) through
// stateRule, and every KeyId accepts the forms its operation takes: key id,
// key ARN, alias name and alias ARN, or only the first two.
//
// Decisions where neither the architecture nor an AWS recording settles the
// behavior:
//
//   - The ciphertext blob is understudy's own versioned envelope: the key
//     ARN, a nonce and the AES-256-GCM ciphertext, with the header and a
//     canonical encoding of the encryption context as additional
//     authenticated data. AWS's format is undocumented and no client parses
//     it.
//   - Scheduled deletion arms a timer for the deletion date rather than
//     deleting lazily on read, so the key and its aliases are gone the
//     instant an Advance passes the date and snapshots re-arm it (Rearm).
//   - A cryptographic operation on a key pending deletion answers
//     KMSInvalidStateException; AWS documents DisabledException too (the
//     fidelity switch kms.pending-deletion.crypto-error).
//   - An alias/aws/<service> alias of an AWS service names an AWS managed
//     key, created on first use by an operation of the key's own account
//     that resolves the alias; an alias ARN from another account and the
//     KeyId of Decrypt and ReEncrypt only find one that exists. ListAliases
//     does not list predefined aliases that name no key yet.
//   - Operations that change a key (tags, rotation, enable, disable,
//     description, key policy, scheduled deletion), CreateAlias and
//     UpdateAlias targeting one, and DeleteAlias of an alias/aws/ alias
//     answer UnsupportedOperationException for AWS managed keys. AWS
//     refuses them through the managed key's fixed key policy, which
//     phase 1 does not evaluate; the text is understudy's own.
//   - The key management operations, which AWS documents as not usable
//     across accounts, refuse another account's key ARN with
//     NotFoundException (the fidelity switch
//     kms.cross-account.management-error); DescribeKey,
//     GetKeyRotationStatus and the cryptographic operations resolve it.
//   - Operations that only read a key read its cell and run the
//     cryptography after the read, so concurrent Encrypt and Decrypt calls
//     never wait on one another.
//   - Rotation is stored and reported (status, period, next date); key
//     material is never rotated, and the next date moves on by whole
//     periods as the clock passes each one.
//   - Key specs other than SYMMETRIC_DEFAULT, other key usages and origins,
//     custom key stores, multi-Region keys and Recipient answer
//     UnsupportedOperationException with an understudy message, never an
//     imitation of the behavior.
//   - The texts AWS has not been recorded sending (alias not found, alias as
//     a target, the reserved alias/aws/ prefix, GenerateDataKey with both
//     KeySpec and NumberOfBytes) are in errors.go with their sources.
package kms
