package sqs

import (
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The timer kinds SQS registers. Each key's ID names the queue by ARN, so
// the handler finds its cell.
const (
	// TimerVisibility fires when a message becomes receivable: its delay
	// or its visibility timeout ends. ID "<queue-arn>/<message-id>".
	TimerVisibility kernel.TimerKind = "sqs.visibility"
	// TimerRetention fires when a message reaches the queue's
	// MessageRetentionPeriod. ID "<queue-arn>/<message-id>".
	TimerRetention kernel.TimerKind = "sqs.retention"
	// TimerMoveTask fires when a running message move task moves its next
	// messages. ID "<source-queue-arn>/<task-id>".
	TimerMoveTask kernel.TimerKind = "sqs.move-task"
)

// DeletedRecently is the sqs.queueDeletedRecently switch: whether
// CreateQueue refuses a name deleted less than 60 seconds ago.
type DeletedRecently uint8

// DeletedRecently values.
const (
	// DeletedRecentlyIgnore reuses a deleted name at once, the default,
	// so a Terraform destroy then apply does not wait (ARCHITECTURE.md 9,
	// provisioning settle).
	DeletedRecentlyIgnore DeletedRecently = iota
	// DeletedRecentlyEnforce answers QueueDeletedRecently for 60 seconds
	// of emulator time after a delete, as AWS does (sqs.md 2.4).
	DeletedRecentlyEnforce
)

// String returns the switch value: "ignore" or "enforce".
func (d DeletedRecently) String() string {
	if d == DeletedRecentlyEnforce {
		return "enforce"
	}
	return "ignore"
}

// Options are the SQS settings wire.go fills from the configuration. The
// zero value is the default behavior.
type Options struct {
	DeletedRecently DeletedRecently
}

// Service is Amazon SQS: one state cell per account and region.
type Service struct {
	k     *kernel.Kernel
	ids   *ids.Source
	opts  Options
	cells *store.Partitioned[aws.Scope, State]
	// The timer kinds as registered with the kernel.
	visibility, retention, moveTask kernel.HandledKind
}

// New binds every SQS operation and the internal HoldVisibility, and
// registers the timer kinds.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{
		k:     d.Kernel,
		ids:   d.IDs,
		opts:  o,
		cells: store.NewPartitioned(d.Kernel.Gate(), codec(), newState),
	}
	s.visibility = d.Kernel.Timers().Handle(TimerVisibility, s.onVisibility)
	s.retention = d.Kernel.Timers().Handle(TimerRetention, s.onRetention)
	s.moveTask = d.Kernel.Timers().Handle(TimerMoveTask, s.onMoveTask)
	b := sqsapi.NewBinder(d.Binder)
	s.bindQueues(b)
	s.bindPermissions(b)
	s.bindSends(b)
	s.bindReceives(b)
	s.bindVisibility(b)
	s.bindMoveTasks(b)
	return s
}

// staleReceipt is the behavior of DeleteMessage with an older receipt
// handle of a message received again since: AWS documents that it
// "will succeed, but the message might not be deleted"; understudy
// succeeds without deleting.
func staleReceipt() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "sqs.receipt.stale-handle",
		Value: "succeed-without-delete",
		Label: evidence.AWSDoc,
		Ref:   "docs/research/phase1/sqs.md#72-receipt-handles-deletemessage-changemessagevisibility",
	}
}

// movedIdentity is what a message move task does to a message: the
// documentation says redriven messages are new messages with a new
// MessageId and enqueue time; LocalStack keeps the id, and no recording
// settles it (sqs.md 9.3).
func movedIdentity() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "sqs.move-task.message-identity",
		Value: "new-message",
		Label: evidence.AWSDoc,
		Ref:   "docs/research/phase1/sqs.md#93-startmessagemovetask-listmessagemovetasks-cancelmessagemovetask",
	}
}

// The sqs.md sections the switches below cite.
const (
	refAttributes = "docs/research/phase1/sqs.md#4-queue-attributes"
	refBatches    = "docs/research/phase1/sqs.md#62-sendmessagebatch-and-the-other-batch-apis"
	refReceipts   = "docs/research/phase1/sqs.md#72-receipt-handles-deletemessage-changemessagevisibility"
	refDedup      = "docs/research/phase1/sqs.md#82-deduplication"
	refFormats    = "docs/research/phase1/sqs.md#3-identifiers-and-formats"
)

// visibilityCeiling is where the 12-hour ChangeMessageVisibility ceiling
// counts from: the documentation says the first receive; understudy
// counts from the latest, which issued the handle and started the
// visibility window being changed. No recording settles it (sqs.md 7.2).
func visibilityCeiling() fidelity.Switch[string] {
	return fidelity.Switch[string]{Name: "sqs.visibility.ceiling-from", Value: "latest-receive", Label: evidence.Inferred, Ref: refReceipts}
}

// fifoReceiptExpiry is which calls a FIFO receipt handle expires for once
// its visibility timeout ends: recorded for DeleteMessage, applied to
// ChangeMessageVisibility too (sqs.md 7.2).
func fifoReceiptExpiry() fidelity.Switch[string] {
	return fidelity.Switch[string]{Name: "sqs.fifo.receipt-expiry", Value: "delete-and-change-visibility", Label: evidence.Inferred, Ref: refReceipts}
}

// fifoDuplicate is what a send suppressed as a FIFO duplicate answers: the
// original's MessageId and SequenceNumber, as both emulators do; AWS's
// fields are unrecorded (sqs.md 8.2).
func fifoDuplicate() fidelity.Switch[string] {
	return fidelity.Switch[string]{Name: "sqs.fifo.duplicate", Value: "original-message-id-and-sequence", Label: evidence.Inferred, Ref: refDedup}
}

// contentDedupFormat is how a content-based MessageDeduplicationId reads:
// the SHA-256 of the body is documented, its rendering is not (sqs.md
// 8.2).
func contentDedupFormat() fidelity.Switch[string] {
	return fidelity.Switch[string]{Name: "sqs.fifo.content-dedup-id", Value: "lowercase-hex-sha256", Label: evidence.Inferred, Ref: refDedup}
}

// sequenceFormat is the SequenceNumber form: a 20-digit decimal counting
// per queue, so numbers order as strings too; AWS documents only a
// 128-bit-range decimal increasing within a group (sqs.md 3).
func sequenceFormat() fidelity.Switch[string] {
	return fidelity.Switch[string]{Name: "sqs.fifo.sequence-number", Value: "20-digit-decimal-per-queue", Label: evidence.Inferred, Ref: refFormats}
}

// batchUnsetVisibility is what a ChangeMessageVisibilityBatch entry
// without a VisibilityTimeout sets: 0, the value an unset integer
// serializes to; no source records it (sqs.md 6.2).
func batchUnsetVisibility() fidelity.Switch[string] {
	return fidelity.Switch[string]{Name: "sqs.batch.unset-visibility-timeout", Value: "zero", Label: evidence.Inferred, Ref: refBatches}
}

// policyLimits is which documented Policy limits are enforced: the 8,192
// bytes and 20 statements, by CreateQueue and SetQueueAttributes
// (InvalidAttributeValue) and AddPermission (OverLimit) alike. The
// principal, condition and per-statement action counts are not, as the
// documentation does not say what each counts (sqs.md 4).
func policyLimits() fidelity.Switch[string] {
	return fidelity.Switch[string]{Name: "sqs.policy.limits", Value: "bytes-and-statements", Label: evidence.AWSDoc, Ref: refAttributes}
}

// Switches lists the behaviors chosen without AWS-recorded evidence.
func (s *Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{
		staleReceipt().Entry(), s.deletedRecently().Entry(), movedIdentity().Entry(),
		visibilityCeiling().Entry(), fifoReceiptExpiry().Entry(), fifoDuplicate().Entry(),
		contentDedupFormat().Entry(), sequenceFormat().Entry(), batchUnsetVisibility().Entry(),
		policyLimits().Entry(),
	}
}

// deletedRecently is the configured QueueDeletedRecently behavior; the
// default departs from AWS on purpose (ARCHITECTURE.md 9).
func (s *Service) deletedRecently() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "sqs.queueDeletedRecently",
		Value: s.opts.DeletedRecently.String(),
		Label: evidence.Inferred,
		Ref:   "docs/design/ARCHITECTURE.md#9-the-clock",
	}
}
