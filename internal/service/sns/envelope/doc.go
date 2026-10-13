// Package envelope renders the JSON envelope SNS wraps around a message for
// non-raw delivery, signs it, and makes the signing key and certificate of
// an account and region.
//
// The builders are pure functions over a Notification (sns.md 5):
//
//   - SQS is the standard-topic body of an SQS message: pretty-printed with
//     "Key" : value pairs in the recorded order, signed (5.1).
//   - SQSFIFO is the FIFO-topic body: SequenceNumber after MessageId and no
//     SignatureVersion, Signature or SigningCertURL (5.3).
//   - Lambda is the Records event of an SNS-triggered function: compact, with
//     SigningCertUrl and UnsubscribeUrl in the recorded casing, Subject null
//     when absent, MessageAttributes {} when empty, and SignatureVersion "1"
//     whatever the topic says, though the signature follows the topic's
//     version (5.4).
//   - SQSConfirmation is the SubscriptionConfirmation an SQS endpoint gets
//     when its subscription waits for ConfirmSubscription: the members of
//     5.5 in the layout of SQS, signed.
//
// StringToSign and ConfirmationStringToSign are the canonical strings of
// 5.6; a Signer signs them with
// SHA1withRSA for version 1 and SHA256withRSA for version 2. NewKey makes an
// RSA key from crypto/rand and a self-signed certificate for it, which the
// SNS service persists per account and region and serves at SigningCertURL.
//
// Decisions the research leaves open:
//
//   - The layout of the MessageAttributes object in the SQS body (one
//     attribute per line, its value compact) is inferred from AWS samples;
//     the recordings store the parsed body, not its bytes. The
//     SubscriptionConfirmation body takes the notification's layout, also
//     inferred: no recording shows its bytes. Attributes are
//     written in the order given; the SNS service sorts them by name, since
//     AWS's own order is not stable.
//   - Strings are escaped as jsonx.AppendString escapes them: the quote, the
//     backslash and control characters. AWS's Jackson writer may spell a
//     control character's escape with upper-case hex.
//   - The certificate is valid from 2000 to 2100 so that a verifier checking
//     it against wall time accepts it whatever the emulator clock says.
package envelope
