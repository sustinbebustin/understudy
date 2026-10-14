// Package outbox is the delivery engine: a Stateful outbox cell that makes
// delivery attempts on the emulator clock, routes failures to dead-letter
// queues and runs each attempt through kernel.Invoke (ARCHITECTURE.md 16,
// ADR 0015).
//
// # Lifecycle of a delivery
//
// A Deliver effect reaches Outbox.Deliver after the source cell unlocks and
// before the source call returns, so an accepted delivery is state from
// then on. Enqueue inserts an Entry into the cell of the target's account
// and region and arms its TimerAttempt timer at the acceptance instant.
// When the timer fires, the attempt runs as kernel.DeliveryAttempt work,
// which Settle counts:
//
//  1. Delivery-phase fault rules apply; a Drop ends the delivery with no
//     attempt, a Delay holds it for wall time. They are evaluated when the
//     timer fires, before the work starts, so attempts due together hit
//     NextN budgets and seeded Probability draws in firing order. The
//     attempt then joins the sequencer, which runs attempts one at a time
//     in that order, so the request-phase rules and seeded ids of their
//     target calls follow it too.
//  2. When a PolicySource is set, access.Advise checks the target's stored
//     resource policy and a would-deny becomes a journal Advisory; the
//     attempt is made anyway.
//  3. The target operation (SQS SendMessage, Lambda Invoke, SNS Publish)
//     runs through kernel.Invoke as the delivering service principal, with
//     the mechanism's Via, admitted at the due instant.
//  4. Success removes the entry. A server-side failure (a retryable or
//     throttling error, a server fault, a 5xx or a 429) is retried on the
//     request's policy; a client-side failure (QueueDoesNotExist,
//     ResourceNotFoundException, any other 4xx) is not (sns.md 7.1). An
//     exhausted or client-side failure goes to the dead-letter queue when
//     one is set and is dropped otherwise. An operation understudy does
//     not implement drops the delivery with its message.
//  5. A journal.Delivery event records the attempt with the source call as
//     its parent.
//
// # Decisions the architecture leaves open
//
// Construction is two-phase, New then Bind, because the kernel takes its
// DeliverSink in kernel.New and the outbox needs that kernel for timers,
// tracked work and Invoke. Bind also takes the fault stage, which the
// kernel does not expose, and the PolicySource, which is built from
// services constructed after the kernel.
//
// The retry policy is persisted beside the request as a policyRecord,
// because delivery.EventBridgeTarget holds a time.Duration, which has no
// JSON form.
//
// Entries are partitioned by the target's scope: every target ARN names an
// account and region (an S3 bucket source does not), the attempt runs
// there, and a reset of an account drops the deliveries pending into it.
//
// Retry delays count from the failed attempt's due instant, never from when
// its goroutine ran, so a retry storm lands on the same instants however an
// Advance interleaves with the attempts. The schedules:
//
//   - SNSManaged follows sns.md 7.2 without jitter: 3 retries at once, 2 at
//     1 s, 10 at 1 s * 20^(k/9) for k = 0..9 truncated to the millisecond
//     (AWS publishes the ends of the backoff, not the series), then 100,000
//     at 20 s.
//   - EventBridgeTarget doubles from 1 s up to 5 minutes with equal jitter
//     (half the delay fixed, half drawn) and stops after MaxAttempts retries
//     or when the next attempt would come later than MaxAge after
//     acceptance; EventBridge documents 185 attempts over 24 hours with
//     exponential backoff and jitter, not the series.
//   - S3Notification retries 3 times, 1 s, 2 s and 4 s apart, then drops.
//   - None makes one attempt.
//
// Jitter is a pure function of the retry number and a per-delivery seed
// drawn once from the ids.KindJitter stream when an EventBridge target's
// delivery is accepted (Entry.Jitter), so it does not depend on the order
// concurrent attempts run in and a seeded run replays it. Other policies
// have no jitter and draw nothing.
//
// Entries are listed and rearmed by acceptance instant, then by Seq, a
// counter of the outbox drawn under the cell lock at insertion, so the
// fan-out of one publish keeps the order the producer delivered it in;
// Import raises the counter past every loaded entry. Deliveries into one
// message group of a FIFO queue form a lane, ordered by Seq alone, the
// order Enqueue arms it in, so a live run and a reload agree on its head
// even when deliveries reached the outbox out of commit order. Only the
// head is armed, a retry keeps the lane blocked, and a terminal outcome
// arms the next, due no earlier than the attempt that ended the previous
// one, so a group arrives in order (sns.md 10). A message group of a
// standard queue promises no order (sqs.md 8.5) and forms no lane.
//
// Attempts run one at a time in firing order on the sequencer, so the same
// seed, rule set and call order give the same fault hits and ids however
// the Go scheduler runs goroutines (ADR 0018). The target calls do not
// block on anything but their target's cell: an SQS SendMessage, an SNS
// Publish, whose own fan-out comes back through the outbox, and an Event
// invoke, which Lambda queues. Two kinds of attempt would hold the
// sequencer for wall time and run on their own goroutine instead, outside
// that order: one a Delivery-phase latency rule delays, whose order
// against the others depends on wall time anyway, and a RequestResponse
// invoke, which waits for the function (no producer enqueues one).
// A delivery has at most one attempt in flight: the attempt claims its
// entry (Entry.Running) until it records its outcome, so a timer that fires
// again meanwhile, as a Rearm does for every armable entry, starts
// no second attempt. The claim is never saved; an entry a snapshot load
// brings back is attempted afresh.
//
// A dead letter is the payload the target would have received: for SQS the
// body (the SNS envelope, or the raw message with its attributes as SQS
// message attributes, sns.md 7.4) with group and deduplication ids kept for
// a FIFO dead-letter queue; a function's event or a topic's message
// otherwise. An EventBridge dead letter adds the String attributes
// RULE_ARN, TARGET_ARN, ERROR_CODE, ERROR_MESSAGE, RETRY_ATTEMPTS and, when
// retries ran out, EXHAUSTED_RETRY_CONDITION (eb-rule-dlq). A dead letter
// the queue refuses is dropped, both reasons journaled.
//
// # Known gaps
//
// An SNS-to-Lambda dead letter's body is the Lambda Records event. AWS
// puts the SQS-style notification envelope there (Type, MessageId,
// TopicArn, ..., SigningCertURL, UnsubscribeURL), which only the SNS
// envelope builder can produce; closing the gap needs the producer to hand
// the outbox its dead letter in delivery.Request.
//
// A FIFO lane is ordered by Seq, drawn when Enqueue runs, after the source
// cell unlocked. Two publishes to one FIFO topic and group that commit
// concurrently can reach Enqueue in the reverse of their commit order, so
// the queue receives them in the reverse of their SNS sequence numbers.
// Closing the gap needs the kernel to stamp each delivery with an order
// drawn under the source cell's lock and hand it over in kernel.Delivery.
//
// A request-phase latency rule matching a target call holds the sequencer
// for its wall time, so every delivery due after it waits too, not only
// those to the matched resource. The outbox cannot hand the call off once
// its fault decision is taken, because the kernel evaluates request-phase
// rules inside kernel.Invoke and reports nothing before the delay; closing
// the gap needs the kernel to signal that decision.
//
// The attempt's journal events (Delivery, Advisory, FaultInjected) and its
// target call carry the source call's cause: the call resumes the entry's
// stored cause through kernel.Target.Resume, so the target call and
// everything it causes stay in the source call's trace, beside the
// Delivery event, rather than under the attempt timer's TimerFired event.
package outbox
