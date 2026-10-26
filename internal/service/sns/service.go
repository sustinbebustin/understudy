package sns

import (
	"time"

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
	// tlsPort is Options.TLSPort: where HTTP deliveries' links point.
	tlsPort func() (uint16, bool)
}

// New binds the SNS operations understudy implements. Every other SNS
// operation (SMS, mobile push, data protection) stays unbound and answers
// 501.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{
		clock:    d.Clock,
		ids:      d.IDs,
		cells:    store.NewPartitioned(d.Kernel.Gate(), codec(), newState),
		policies: filterpolicy.NewCache(),
		tlsPort:  o.TLSPort,
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
// subscription: "PendingConfirmation" in place of the ARN (sns.md 3.4),
// which a third-party recording shows for an email subscription
// (sns-http-delivery.md 2.2). No project recording lists a pending
// subscription, so it stays a switch.
func pendingInListings() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    "sns.listing.pending-arn",
		Value:   "PendingConfirmation",
		Label:   evidence.Inferred,
		Ref:     "docs/research/sns.md#34-listing",
		Ops:     []string{"sns.ListSubscriptions", "sns.ListSubscriptionsByTopic"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: no recording lists a pending subscription"},
	}
}

// publishOps are the operations that publish, where the FIFO and Lambda
// delivery switches apply.
func publishOps() []string { return []string{"sns.Publish", "sns.PublishBatch"} }

// fifoSequence is the text of a FIFO SequenceNumber: a 20-digit decimal
// counter per topic, as LocalStack renders it; AWS documents a 128-bit
// increasing number without its text (sns.md 4.3).
func fifoSequence() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    "sns.fifo.sequence-number",
		Value:   "20-digit-decimal-per-topic",
		Label:   evidence.Inferred,
		Ref:     "docs/research/sns.md#43-fifo-publish",
		Ops:     publishOps(),
		Settles: fidelity.NoScenario{Reason: "sns/R16/04-publish-fifo.json holds one SequenceNumber, which does not show how the numbers count"},
	}
}

// lambdaAttributeTypes is the DataType a Lambda event carries for Number
// and String.Array attributes: the published one, where the docs say
// String (sns.md 9).
func lambdaAttributeTypes() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    "sns.lambda.attribute-type",
		Value:   "as-published",
		Label:   evidence.Inferred,
		Ref:     "docs/research/sns.md#9-conflicts-between-sources",
		Ops:     publishOps(),
		Settles: fidelity.NoScenario{Reason: "logs/R16/01-lambda-events.json is recorded; no scenario replays it, as the delivery runs a Lambda function under Docker"},
	}
}

// fifoDuplicate is what a FIFO publish inside the deduplication window
// answers: a fresh MessageId and SequenceNumber with no delivery, as the
// research recommends; no recording shows whether AWS returns the
// original's (sns.md Implications 10).
func fifoDuplicate() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    "sns.fifo.duplicate",
		Value:   "fresh-message-id-no-delivery",
		Label:   evidence.Inferred,
		Ref:     "docs/research/sns.md#43-fifo-publish",
		Ops:     publishOps(),
		Settles: fidelity.NoScenario{Reason: "unrecorded: no recording shows what a FIFO publish inside the deduplication window answers"},
	}
}

// detachedRetention is how long the subscriptions of a deleted topic stay
// readable through GetSubscriptionAttributes, and removable through
// Unsubscribe. AWS keeps them after DeleteTopic (LocalStack
// test_delete_subscriptions_on_delete_topic, AWS-recorded) for a time no
// source documents; 48 hours is LocalStack's report.
func detachedRetention() fidelity.Switch[time.Duration] {
	return fidelity.Switch[time.Duration]{
		Name:  "sns.delete-topic.detached-retention",
		Value: 48 * time.Hour,
		Label: evidence.Inferred,
		Ref:   "docs/research/sns.md#22-topic-attributes",
		Ops:   []string{"sns.DeleteTopic", "sns.GetSubscriptionAttributes", "sns.Unsubscribe"},
		Settles: fidelity.NoScenario{
			Reason: "sns/R16/06-list-subscriptions-after-delete.json shows the subscriptions 5 s after DeleteTopic; how long they stay is unrecorded",
		},
	}
}

// Switches lists the behaviors chosen without AWS-recorded evidence, http
// delivery's and filter policy evaluation's among them.
func (s *Service) Switches() []fidelity.Entry {
	entries := []fidelity.Entry{
		pendingInListings().Entry(), fifoSequence().Entry(), lambdaAttributeTypes().Entry(), fifoDuplicate().Entry(),
		detachedRetention().Entry(),
	}
	for _, sw := range httpSwitches() {
		entries = append(entries, sw.Entry())
	}
	return append(entries, filterpolicy.Switches()...)
}
