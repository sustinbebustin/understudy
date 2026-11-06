package sqs

import (
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/authz"
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
	// TimerDelay fires when a message's delay ends, before its first
	// receive. ID "<queue-arn>/<message-id>".
	TimerDelay kernel.TimerKind = "sqs.delay"
	// TimerVisibility fires when a received message's visibility timeout
	// ends. ID "<queue-arn>/<message-id>".
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

// Options are the SQS settings the composition root takes from the
// configuration. The zero value is the default behavior.
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
	delay, visibility, retention, moveTask kernel.HandledKind
}

// New binds every SQS operation and the internal HoldVisibility and
// MessageCauses, and registers the timer kinds.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{
		k:     d.Kernel,
		ids:   d.IDs,
		opts:  o,
		cells: store.NewPartitioned(d.Kernel.Gate(), codec(), newState),
	}
	s.delay = d.Kernel.Timers().Handle(TimerDelay, s.onVisibility)
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
// succeeds without deleting. sqs/R01#03-recv-after-6s contradicts it: AWS
// deleted the message with the older handle, so the receive after its
// visibility timeout finds none (docs/guide/differences.md).
func staleReceipt() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    "sqs.receipt.stale-handle",
		Value:   "succeed-without-delete",
		Label:   evidence.AWSDoc,
		Ref:     "docs/research/sqs.md#72-receipt-handles-deletemessage-changemessagevisibility",
		Ops:     []string{"sqs.DeleteMessage", "sqs.DeleteMessageBatch"},
		Settles: fidelity.SettledBy{Step: fidelity.StepRef{Scenario: "sqs/R01", Step: "03-recv-after-6s"}},
	}
}

// movedIdentity is what a message move task does to a message: the
// documentation says redriven messages are new messages with a new
// MessageId and enqueue time; LocalStack keeps the id (sqs.md 9.3).
func movedIdentity() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    "sqs.move-task.message-identity",
		Value:   "new-message",
		Label:   evidence.AWSDoc,
		Ref:     "docs/research/sqs.md#93-startmessagemovetask-listmessagemovetasks-cancelmessagemovetask",
		Ops:     []string{"sqs.StartMessageMoveTask"},
		Settles: fidelity.NoScenario{Reason: "sqs/R02/std-06-recv-src.json and fifo-06-recv-src.json are recorded; no scenario replays sqs/R02 yet"},
	}
}

// The sqs.md sections the switches below cite.
const (
	refAttributes = "docs/research/sqs.md#4-queue-attributes"
	refBatches    = "docs/research/sqs.md#62-sendmessagebatch-and-the-other-batch-apis"
	refReceipts   = "docs/research/sqs.md#72-receipt-handles-deletemessage-changemessagevisibility"
	refDedup      = "docs/research/sqs.md#82-deduplication"
	refFormats    = "docs/research/sqs.md#3-identifiers-and-formats"
)

// visibilityCeiling is where the 12-hour ChangeMessageVisibility ceiling
// counts from: the documentation says the first receive; understudy
// counts from the latest, which issued the handle and started the
// visibility window being changed. No recording settles it (sqs.md 7.2).
func visibilityCeiling() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name: "sqs.visibility.ceiling-from", Value: "latest-receive", Label: evidence.Inferred, Ref: refReceipts,
		Ops:     []string{"sqs.ChangeMessageVisibility", "sqs.ChangeMessageVisibilityBatch"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: no recording reaches the 12-hour ceiling"},
	}
}

// fifoReceiptExpiry is which calls a FIFO receipt handle expires for once
// its visibility timeout ends: recorded for DeleteMessage, applied to
// ChangeMessageVisibility too (sqs.md 7.2).
func fifoReceiptExpiry() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name: "sqs.fifo.receipt-expiry", Value: "delete-and-change-visibility", Label: evidence.Inferred, Ref: refReceipts,
		Ops: []string{"sqs.DeleteMessage", "sqs.DeleteMessageBatch", "sqs.ChangeMessageVisibility", "sqs.ChangeMessageVisibilityBatch"},
		Settles: fidelity.NoScenario{
			Reason: "sqs/R01#07-fifo-delete-expired records the DeleteMessage half only; ChangeMessageVisibility on an expired FIFO handle is unrecorded",
		},
	}
}

// fifoDuplicate is what a send suppressed as a FIFO duplicate answers: the
// original's MessageId and SequenceNumber, as both emulators do; AWS's
// fields are unrecorded (sqs.md 8.2).
func fifoDuplicate() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name: "sqs.fifo.duplicate", Value: "original-message-id-and-sequence", Label: evidence.Inferred, Ref: refDedup,
		Ops:     []string{"sqs.SendMessage", "sqs.SendMessageBatch"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: no recording shows what a suppressed FIFO duplicate send answers"},
	}
}

// contentDedupFormat is how a content-based MessageDeduplicationId reads:
// the SHA-256 of the body is documented, its rendering is not (sqs.md
// 8.2). It is computed on send and read back on receive.
func contentDedupFormat() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name: "sqs.fifo.content-dedup-id", Value: "lowercase-hex-sha256", Label: evidence.Inferred, Ref: refDedup,
		Ops: []string{"sqs.SendMessage", "sqs.SendMessageBatch", "sqs.ReceiveMessage"},
		Settles: fidelity.NoScenario{
			Reason: "sqs/R01#07-fifo-recv1 holds the value, but the step differs on JSON member order, so a claim there would read contradicted for another reason",
		},
	}
}

// sequenceFormat is the SequenceNumber form: a 20-digit decimal counting
// per queue, so numbers order as strings too; AWS documents only a
// 128-bit-range decimal increasing within a group (sqs.md 3).
func sequenceFormat() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name: "sqs.fifo.sequence-number", Value: "20-digit-decimal-per-queue", Label: evidence.Inferred, Ref: refFormats,
		Ops:     []string{"sqs.SendMessage", "sqs.SendMessageBatch", "sqs.ReceiveMessage"},
		Settles: fidelity.NoScenario{Reason: "sqs/R01#07-fifo-recv1 records the 20-digit form but not how the numbers count"},
	}
}

// batchUnsetVisibility is what a ChangeMessageVisibilityBatch entry
// without a VisibilityTimeout sets: 0, the value an unset integer
// serializes to; no source records it (sqs.md 6.2).
func batchUnsetVisibility() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name: "sqs.batch.unset-visibility-timeout", Value: "zero", Label: evidence.Inferred, Ref: refBatches,
		Ops:     []string{"sqs.ChangeMessageVisibilityBatch"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: sqs/R01#06-cmv-batch sets VisibilityTimeout on every entry"},
	}
}

// policyLimits is which documented Policy limits are enforced: the 8,192
// bytes and 20 statements, by CreateQueue and SetQueueAttributes
// (InvalidAttributeValue) and AddPermission (OverLimit) alike. The
// principal, condition and per-statement action counts are not, as the
// documentation does not say what each counts (sqs.md 4).
func policyLimits() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name: "sqs.policy.limits", Value: "bytes-and-statements", Label: evidence.AWSDoc, Ref: refAttributes,
		Ops:     []string{"sqs.CreateQueue", "sqs.SetQueueAttributes", "sqs.AddPermission"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: no recording exceeds a Policy limit"},
	}
}

// Switches lists the behaviors chosen without AWS-recorded evidence, with
// SQS's denial identity per protocol (iam-enforcement-surfaces.md 12.8).
func (s *Service) Switches() []fidelity.Entry {
	authorized := s.k.Bindings().AuthorizedOps(sqsapi.Service())
	return []fidelity.Entry{
		staleReceipt().Entry(), s.deletedRecently().Entry(), movedIdentity().Entry(),
		visibilityCeiling().Entry(), fifoReceiptExpiry().Entry(), fifoDuplicate().Entry(),
		contentDedupFormat().Entry(), sequenceFormat().Entry(), batchUnsetVisibility().Entry(),
		policyLimits().Entry(),
		authz.RecordedSwitch("sqs.denial.json-identity", "header AccessDenied;Sender, __type com.amazon.coral.service#AccessDeniedException, 403",
			"3.2", "IAM-ENF-01", authz.SessionUnreplayed, authorized),
		// The Query text is the grammar SQS writes over JSON, not the
		// aws-doc "Access to the resource <url> is denied." (4.3).
		authz.RecordedSwitch("sqs.denial.query-text", "AccessDenied, Sender, 403, the grammar of 4.1 with the action in lower case",
			"4.3", "IAM-ENF-02", authz.SessionUnreplayed, authorized),
		authz.RecordedSwitch("sqs.enforce.missing-resource.delivery", "resolve-first", "8.2", "IAM-ENF-18", authz.DeliveryUnreplayed,
			s.k.Bindings().DeliveryResolvedFirstOps(sqsapi.Service())),
	}
}

// deletedRecently is the configured QueueDeletedRecently behavior; the
// default departs from AWS on purpose (ARCHITECTURE.md 9).
func (s *Service) deletedRecently() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    "sqs.queueDeletedRecently",
		Value:   s.opts.DeletedRecently.String(),
		Label:   evidence.Inferred,
		Ref:     "docs/ARCHITECTURE.md#9-the-clock",
		Ops:     []string{"sqs.CreateQueue"},
		Settles: fidelity.NoScenario{Reason: "a deliberate departure from AWS (ARCHITECTURE.md 9), which a recording does not change"},
	}
}
