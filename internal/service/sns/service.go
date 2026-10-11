package sns

import (
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/sns/filterpolicy"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Service is Amazon SNS: one state cell per account and region.
type Service struct {
	// clock gives reads outside a write the instant pending subscriptions
	// expire against.
	clock   *clock.Clock
	ids     *ids.Source
	cells   *store.Partitioned[aws.Scope, State]
	signers signerCache
	// policies holds the subscriptions' filter policies compiled, filled
	// where a policy is set and on its first publish after a load.
	policies *filterpolicy.Cache
}

// New binds the SNS operations understudy implements. Every other SNS
// operation (SMS, mobile push, data protection) stays unbound and answers
// 501.
func New(d kernel.ServiceDeps, _ Options) *Service {
	s := &Service{
		clock:    d.Clock,
		ids:      d.IDs,
		cells:    store.NewPartitioned(d.Kernel.Gate(), codec(), newState),
		policies: filterpolicy.NewCache(),
	}
	b := snsapi.NewBinder(d.Binder)
	s.bindTopics(b)
	s.bindTags(b)
	s.bindPermissions(b)
	s.bindSubscriptions(b)
	s.bindSubscriptionAttributes(b)
	s.bindPublish(b)
	s.bindLinks(b)
	return s
}

// pendingInListings is how ListSubscriptions shows a pending
// subscription: AWS's widely observed "PendingConfirmation" in place of the
// ARN, which no primary source documents (sns.md 3.4).
func pendingInListings() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "sns.listing.pending-arn",
		Value: "PendingConfirmation",
		Label: evidence.Inferred,
		Ref:   "docs/research/phase1/sns.md#34-listing",
	}
}

// fifoSequence is the text of a FIFO SequenceNumber: a 20-digit decimal
// counter per topic, as LocalStack renders it; AWS documents a 128-bit
// increasing number without its text (sns.md 4.3).
func fifoSequence() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "sns.fifo.sequence-number",
		Value: "20-digit-decimal-per-topic",
		Label: evidence.Inferred,
		Ref:   "docs/research/phase1/sns.md#43-fifo-publish",
	}
}

// lambdaAttributeTypes is the DataType a Lambda event carries for Number
// and String.Array attributes: the published one, where the docs say
// String and no recording exists (sns.md 9).
func lambdaAttributeTypes() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "sns.lambda.attribute-type",
		Value: "as-published",
		Label: evidence.Inferred,
		Ref:   "docs/research/phase1/sns.md#9-conflicts-between-sources",
	}
}

// fifoDuplicate is what a FIFO publish inside the deduplication window
// answers: a fresh MessageId and SequenceNumber with no delivery, as the
// research recommends; no recording shows whether AWS returns the
// original's (sns.md Implications 10).
func fifoDuplicate() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "sns.fifo.duplicate",
		Value: "fresh-message-id-no-delivery",
		Label: evidence.Inferred,
		Ref:   "docs/research/phase1/sns.md#43-fifo-publish",
	}
}

// Switches lists the behaviors chosen without AWS-recorded evidence,
// filter policy evaluation's among them.
func (s *Service) Switches() []fidelity.Entry {
	return append([]fidelity.Entry{pendingInListings().Entry(), fifoSequence().Entry(), lambdaAttributeTypes().Entry(), fifoDuplicate().Entry()},
		filterpolicy.Switches()...)
}
