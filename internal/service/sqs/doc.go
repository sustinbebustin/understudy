// Package sqs implements Amazon SQS: standard and FIFO queues, long
// polling, visibility, receipt handles, retention, redrive to dead-letter
// queues and message move tasks, every operation of the model plus the
// internal HoldVisibility and MessageCauses. State is one cell per account
// and region; a dead-letter queue lives in its sources' cell, so a redrive
// or a move changes one cell under one lock. Queues are value records: a write
// changes a working copy and stores it back (ARCHITECTURE.md 3.5). The
// copy shares the stored messages and caches until the write first
// changes them, so a send copies no queued message and filling a queue
// stays linear (Queue.clone, DedupCache). A
// message keeps the journal event of the call that enqueued it.
// ReceiveMessage is the long-poll reference of ARCHITECTURE.md 8.
//
// Timers, all on the emulator clock and re-armed by Rearm from the state:
// sqs.visibility fires when a message becomes receivable (its delay or its
// visibility timeout ends) and wakes the queue's receivers; sqs.retention
// deletes a message at the end of the queue's MessageRetentionPeriod, in
// flight or not, and is re-armed for every message when SetQueueAttributes
// changes the period; sqs.move-task moves a running task's next messages.
//
// Decisions the architecture leaves open (Service.Switches lists those
// without recorded evidence):
//
//   - The default MaximumMessageSize is 1 MiB, the recorded default and
//     the model's limit (sqs.md 4 and 11), not the 256 KiB of the General
//     Reference; the size counts the body and every message attribute's
//     name, type and value.
//   - A receipt handle is well formed when it has the form understudy
//     mints (AQEB and 32 base64 characters); anything else is
//     ReceiptHandleIsInvalid. A well-formed handle that matches no message
//     deletes nothing and succeeds (sqs.receipt.stale-handle). A FIFO
//     handle expires with its visibility timeout for DeleteMessage, as
//     recorded, and for ChangeMessageVisibility too
//     (sqs.fifo.receipt-expiry).
//   - Redrive happens on the receive that would exceed maxReceiveCount
//     (sqs.md 9.2). A policy whose dead-letter queue no longer exists, or
//     names the queue itself, moves nothing. The receive's journal event
//     names both queues, and the moved message's enqueue event is the
//     receive, so what its delivery leads to descends from the receive. No
//     journal event records the move itself, nor a move task's moves, so
//     the journal edge from the receive to the dead-letter send that
//     ARCHITECTURE.md 10 lists is missing: a journal.Delivery needs a
//     delivery.Mechanism, and the closed set of ARCHITECTURE.md 16 has
//     none for a move inside SQS.
//   - The 12-hour ChangeMessageVisibility ceiling counts from the receive
//     that issued the handle, not the first receive sqs.md 7.2 quotes:
//     every receive issues a new handle and starts a new visibility window
//     (sqs.visibility.ceiling-from).
//   - ReceiveRequestAttemptId applies to FIFO queues and is ignored on
//     standard ones. A retry returns the attempt's messages and handles
//     and restarts their visibility timeout while each still holds the
//     handle the attempt gave it and no ChangeMessageVisibility touched
//     it; otherwise the retry is a new attempt.
//   - A message move task moves its first messages when it starts, then
//     MaxNumberOfMessagesPerSecond more every second of emulator time;
//     without a rate it moves everything in one step. It takes the oldest
//     messages whatever their visibility, so an in-flight message's handle
//     goes stale, and moves each as a new message
//     (sqs.move-task.message-identity). A destination that does not exist
//     fails the task with AWS.SimpleQueueService.NonExistentQueue; one
//     that is the source is refused at the start. Without a DestinationArn
//     a task moves only messages dead-lettered from another queue: one
//     sent straight to the dead-letter queue has no source to go back to,
//     so it stays, and ApproximateNumberOfMessagesToMove leaves it out.
//     Cancelling is immediate, so no task is seen CANCELLING. A source
//     queue keeps its ten newest tasks, ListMessageMoveTasks' cap. Task
//     ids are UUIDs drawn from the message id stream, as EventBridge event
//     ids are.
//   - A ChangeMessageVisibilityBatch entry without a VisibilityTimeout
//     sets 0, the value an unset integer serializes to
//     (sqs.batch.unset-visibility-timeout).
//   - AddPermission and RemovePermission are stored only: they edit the
//     statements of the Policy attribute in the form AWS writes, which
//     nothing enforces. An action is a modeled operation name or "*"; the
//     label follows the queue name's character rule.
//   - Policy is checked for form (a JSON object), size and statement count
//     (sqs.policy.limits), and AddPermission keeps the same limits, so a
//     policy read back can always be set again; the limit errors say which
//     limit and by how much. RedriveAllowPolicy for form (redrivePermission
//     and sourceQueueArns). Both answer InvalidAttributeValue, as does a
//     RedrivePolicy that is not JSON. A RedrivePolicy missing a key or
//     with a maxReceiveCount out of range answers InvalidParameterValue,
//     the recorded code; its target must exist in the queue's account and
//     region, be of the same type and admit the source.
//   - The stateless limit checks with hand texts (message bodies and
//     attributes, batch entry ids and sizes, visibility timeouts, attempt
//     ids, MaxResults, move rates, permission labels and actions, tag keys
//     and values) run at step 7 through kernel.WithCheck, before fault
//     rules. Every binding names its queue through kernel.WithResources, so
//     fault rules scoped by resource match before the handler runs. Texts
//     no source records are marked "inferred" in errors.go and follow the
//     frame of a recorded sibling.
//   - QueueDeletedRecently is off by default (sqs.queueDeletedRecently): a
//     deleted queue's name is free at once, so a Terraform destroy then
//     apply does not wait; Options.DeletedRecently restores the 60
//     seconds.
package sqs
