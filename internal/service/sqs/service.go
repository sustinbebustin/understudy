package sqs

import (
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerVisibility fires when a message becomes receivable: its delay or its
// visibility timeout ends. The key's ID is "<queue-arn>/<message-id>".
const TimerVisibility kernel.TimerKind = "sqs.visibility"

// Options are the SQS settings wire.go fills from the configuration. The
// zero value is AWS's behavior; there are no settings yet.
type Options struct{}

// Service is Amazon SQS: one state cell per account and region.
type Service struct {
	k     *kernel.Kernel
	ids   *ids.Source
	cells *store.Partitioned[aws.Scope, State]
	// visibility is TimerVisibility as registered with the kernel.
	visibility kernel.HandledKind
}

// New binds the SQS operations understudy implements and registers the
// visibility timer. Every other SQS operation stays unbound and answers 501.
func New(d kernel.ServiceDeps, _ Options) *Service {
	codec := jsonx.NewCodec()
	s := &Service{
		k:     d.Kernel,
		ids:   d.IDs,
		cells: store.NewPartitioned(d.Kernel.Gate(), store.Codec{Marshal: codec.Marshal, Unmarshal: codec.Unmarshal}, newState),
	}
	s.visibility = d.Kernel.Timers().Handle(TimerVisibility, s.onVisibility)
	b := sqsapi.NewBinder(d.Binder)
	s.bindQueues(b)
	s.bindMessages(b)
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

// Switches lists the behaviors chosen without AWS-recorded evidence.
func (s *Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{staleReceipt().Entry()}
}
