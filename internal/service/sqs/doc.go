// Package sqs implements Amazon SQS. Its target is the phase 1 scope:
// standard and FIFO queues, long polling, visibility, redrive, dead-letter
// queues and message move tasks. What exists today is below.
//
// Bound so far: CreateQueue, GetQueueUrl, SendMessage, ReceiveMessage and
// DeleteMessage on standard queues; every other operation answers 501. State
// is one cell per account and region; a message is an immutable record that
// keeps the journal event of the call that sent it. ReceiveMessage is the
// long-poll reference of ARCHITECTURE.md 8.
//
// Decisions the architecture leaves open:
//
//   - FIFO queues are refused with UnsupportedOperation and a text saying
//     so, rather than created with standard semantics; the FIFO-only
//     InvalidParameterValue texts of sqs.md 7.2 (an expired FIFO receipt
//     handle) therefore cannot arise yet.
//   - The default MaximumMessageSize is 1 MiB, the recorded default and the
//     model's limit (sqs.md 4 and 11), not the 256 KiB of the General
//     Reference and the task plan; the size counts the body and every
//     message attribute's name, type and value.
//   - One timer kind, sqs.visibility, covers both a delay and a visibility
//     timeout ending: either makes the message receivable, and its handler
//     wakes the queue's receivers when the message is still there.
//   - Retention is applied when a receive walks the queue: an expired
//     message is dropped then, with its timer.
//   - A receipt handle is well formed when it has the form understudy mints
//     (AQEB and 32 base64 characters); anything else is
//     ReceiptHandleIsInvalid. A well-formed handle that matches no message
//     succeeds (sqs.receipt.stale-handle).
//   - Message attributes are checked for count, data type prefix and
//     non-empty values. Not validated yet, as the SendMessage coverage note
//     says: attribute names, Number values, the format and length of data
//     types, the characters of String values and the data type of
//     AWSTraceHeader; MessageDeduplicationId is ignored on standard queues.
//   - The stateless limit checks with hand texts (the body's characters,
//     the message attributes, ReceiveMessage's VisibilityTimeout range) run
//     at step 7 through kernel.WithCheck, before fault rules, since the
//     generated policy cannot render their codes or texts yet. Every
//     binding names its queue through kernel.WithResources, so fault rules
//     scoped by resource match before the handler runs.
//   - Policy and RedriveAllowPolicy are checked for form only (a JSON
//     object; redrivePermission and sourceQueueArns), answering
//     InvalidAttributeValue. RedrivePolicy answers InvalidParameterValue,
//     the recorded code, with LocalStack's texts, as AWS's are unrecorded
//     (sqs.md 9.1).
package sqs
