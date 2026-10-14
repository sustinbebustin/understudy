// Package notify builds S3 event notifications and routes them to SQS, SNS and
// Lambda targets: it parses a bucket's notification configuration, selects
// the configurations an object change matches, and builds the record
// (eventVersion 2.1, URL-encoded key, unquoted eTag, sequencer) and the
// s3:TestEvent message as delivery requests under the S3Notification retry
// policy (s3.md 13, lambda-events.md 6). The builders are pure; the s3
// package stores the configuration and raises the deliveries.
//
// Decisions the research leaves open:
//
//   - object.key keeps letters, digits, "-_.*" and '/' and encodes a space
//     as '+' and every other byte as %XX: the recordings show a@b as a%40b
//     and a key with slashes unchanged, and the documentation shows a space
//     as '+'. '*' and '~' follow Java's form encoder (inferred).
//   - Prefix and suffix filters match the URL-encoded key, in EncodeKey's
//     form, not the raw key: notification-how-to-filtering.html requires a
//     space in a filter value written as '+' and other special characters
//     URL-encoded, and LocalStack's AWS-validated notifier matches
//     quote(key) (notifications.py L148, L229). The recording
//     test_notifications_with_filter uses keys that encode to themselves, so
//     it cannot tell the two apart.
//   - The sequencer is 18 uppercase hex digits, AWS's width: the change's
//     Unix nanoseconds, or one more than the last sequencer when that is not
//     later, so it strictly increases for a caller that keeps the last one.
//   - A configuration without an Id gets the base64 of a UUID-shaped SHA-256
//     of the bucket, its position and its destination, the shape of AWS's
//     generated ids, without drawing from the seeded id streams.
//   - An unknown event type is InvalidArgument "The event is not supported
//     for notifications" naming Event (inferred; no recording shows it), and
//     two prefix or two suffix rules are InvalidArgument "Cannot specify more
//     than one prefix rule in a filter." naming FilterRule.Name.
//   - Two rules that one change of one key would select both of (their event
//     types share a change, a wildcard covering its family; one prefix
//     starts the other and one suffix ends the other, an absent rule
//     counting as empty) are refused, as notification-how-to-filtering.html
//     documents. Two rules with the same prefix and suffix answer
//     InvalidArgument "Configurations overlap. Configurations on the same
//     bucket cannot share a common event type."; any other overlap
//     "Configuration is ambiguously defined. Cannot have overlapping suffixes
//     in two rules if the prefixes are overlapping for the same event
//     type.". Both texts are ones users report from AWS; which overlap gets
//     which, and the absence of ArgumentName, are inferred.
//   - A destination outside the bucket's region is InvalidArgument "The
//     notification destination service region is not valid for the bucket
//     location constraint", checked even when destination validation is
//     skipped (moto's wording and placement, inferred).
//   - A FIFO queue or topic (name ending .fifo) fails destination
//     validation as "The destination queue is a FIFO queue, which S3 event
//     notifications do not support" (topic: "...topic..."); the documentation
//     forbids them, the wording is inferred.
//   - userIdentity.principalId is "AWS:" and the caller's unique id: the
//     account id for root, the AIDA id for a user, and the AROA id, ':' and
//     the session name for a role session. The content-structure
//     documentation example shows a bare AIDA id and the Lambda with-s3
//     example shows "AWS:AIDA..."; the prefix follows the latter, and the
//     root and role-session shapes are inferred. sourceIPAddress is
//     127.0.0.1, the loopback address understudy listens on, since a call
//     carries no peer address (inferred).
//   - Tagging events are eventVersion 2.3 and carry the eTag without size or
//     sequencer, as recorded. A delete marker's event carries no size or
//     eTag, like every removal.
//   - Fan-out order is document order: topics, then queues, then functions.
//     An EventBridgeConfiguration is stored and read back; nothing is sent to
//     EventBridge.
package notify
