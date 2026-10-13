// Package identity defines Principal and the credential store: key minting and
// the mapping from an access key to the account and principal making a call
// (ADR 0012).
//
// Resolve never decodes a key understudy did not mint. A real-looking AKIA or
// ASIA key decodes to a real account number (AKIAIOSFODNN7EXAMPLE gives
// 581039954779), and mapping a developer's production key to a foreign account
// is the failure ADR 0012 rules out; such keys resolve to the default
// account's root, and Fallback.Warn marks the first sighting of each so the
// caller logs one warning per key.
//
// Decisions the architecture leaves open:
//
//   - Long-term and temporary keys are minted by separate functions,
//     MintLongTerm and MintTemporary(expiry), and the session token lives in
//     the Temporary lifetime, so long-term credentials with an expiry or a
//     token, and temporary ones without, cannot be expressed. Assumed roles take
//     only temporary credentials.
//   - Credentials, Minted and Temporary redact the secret and the token in
//     LogValue, MarshalJSON (for slog's JSONHandler, which json-encodes a
//     struct or slice holding one), String and GoString. Export writes its
//     own records, so it keeps the real values.
//   - Resolve warns about at most 10,000 distinct real-looking keys; past
//     that it stops recording them, so a client cycling random keys cannot
//     grow memory.
//   - Expiry is not enforced by Resolve: phase 1 does not enforce IAM, and
//     Minted carries the Lifetime for a caller that wants to.
//   - Minting and Import refuse a user or role name, session name or path
//     that IAM or STS would refuse, using the rules in package aws that the
//     access recorder also applies, so every principal the store holds can
//     have its calls recorded and renders an ARN that reads back as itself.
//   - Render shows a service principal as its service name and an anonymous
//     caller as "anonymous", since neither has an ARN.
//   - Export writes JSON with encoding/json in the {"kind": ...} tagged-union
//     shape, because the dependency rules keep jsonx out of this package.
//     Snapshots therefore hold minted secrets, which signature verification
//     needs after a load.
package identity
