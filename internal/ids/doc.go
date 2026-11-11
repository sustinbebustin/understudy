// Package ids defines the identifier types (request, message, receipt, upload,
// version, key and UUID) and the seeded source that generates them.
//
// Every emulator-minted identifier comes from a Source. A random Source reads
// crypto/rand; a seeded one gives each Kind its own ChaCha8 stream derived
// from (seed, kind name), so a new call site of one kind never shifts the ids
// of another, and records stream positions for snapshots. KMS key material and
// SNS signing keys are not drawn here: they always come from crypto/rand.
//
// Decisions the architecture leaves open:
//
//   - The package imports nothing in the repository (ARCHITECTURE.md 2), so
//     access key minting takes the account as its 12-digit string and
//     DecodeAccount returns one; package identity converts between it and
//     aws.AccountID.
//   - Stream positions count 64-bit words; a draw of n bytes consumes
//     ceil(n/8) words, and Seek replays the stream from its start. Seek
//     refuses positions above MaxPosition so a corrupt manifest cannot stall
//     snapshot load, and Positions refuses them too so a snapshot is never
//     saved that its own binary cannot load. Drawing itself is unlimited.
//   - Receipt handles are AQEB and the padded base64 of random bytes, 412
//     characters for a standard queue and 304 for a FIFO one, the lengths
//     of AWS's; their structure beyond length and alphabet is unknown. Upload
//     ids are 64 URL-safe base64 characters, session tokens 192 base64
//     characters. AWS documents all three as opaque.
//   - IAM unique ids are random after the prefix; whether AWS embeds the
//     account in them is unverified.
//   - API Gateway ids take the lengths of AWS's samples over [a-z0-9],
//     drawn by rejection sampling so every character is equally likely; an
//     integration id is as long as a route id. The apigw-requestid is
//     derived from the call's request id rather than drawn, so the header
//     and the Lambda event carry one value.
//   - SecretAccessKey and SessionToken redact themselves in log/slog output.
package ids
