package sns

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/authz"
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
	// awsName is Options.AWSName: the hosts HTTP deliveries' links never
	// name.
	awsName func(host string) bool
	// bindings names the operations a denial can answer, for the switches.
	bindings *kernel.Bindings
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
		awsName:  o.AWSName,
		bindings: d.Kernel.Bindings(),
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
// (sns-http-delivery.md 2.2, 8.3.5). No project recording lists a pending
// subscription, so it stays a switch.
func pendingInListings() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    "sns.listing.pending-arn",
		Value:   "PendingConfirmation",
		Label:   evidence.ThirdPartyRecorded,
		Ref:     "docs/research/sns.md#34-listing",
		Ops:     []string{"sns.ListSubscriptions", "sns.ListSubscriptionsByTopic"},
		Settles: fidelity.NoScenario{Reason: "no project recording lists a pending subscription; third-party-recorded (sns-http-delivery.md 2.2)"},
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
// listed by ListSubscriptions, readable through GetSubscriptionAttributes,
// and removable through Unsubscribe. AWS keeps them after DeleteTopic
// (LocalStack test_delete_subscriptions_on_delete_topic, AWS-recorded;
// sns/R16 06-list-subscriptions-after-delete) for a time no source
// documents; 48 hours is LocalStack's report.
func detachedRetention() fidelity.Switch[time.Duration] {
	return fidelity.Switch[time.Duration]{
		Name:  "sns.delete-topic.detached-retention",
		Value: 48 * time.Hour,
		Label: evidence.Inferred,
		Ref:   "docs/research/sns.md#22-topic-attributes",
		Ops:   []string{"sns.DeleteTopic", "sns.GetSubscriptionAttributes", "sns.ListSubscriptions", "sns.Unsubscribe"},
		Settles: fidelity.NoScenario{
			Reason: "sns/R16/06-list-subscriptions-after-delete.json shows the subscriptions 5 s after DeleteTopic; how long they stay is unrecorded",
		},
	}
}

// Switches lists the service's fidelity switches, http delivery's, filter policy evaluation's, and SNS's denial text and
// deliveries (iam-enforcement-surfaces.md 12.8) among them.
func (s *Service) Switches() []fidelity.Entry {
	entries := []fidelity.Entry{
		pendingInListings().Entry(), fifoSequence().Entry(), fifoDuplicate().Entry(),
		detachedRetention().Entry(),
		authz.AccountIDPrincipalSwitch("sns", "IAM-EVAL-25", []string{"sns.CreateTopic", "sns.SetTopicAttributes"}),
		// The code and status are the model's AuthorizationErrorException
		// (aws-doc); the recorded message writes the action "SNS:<Name>".
		authz.RecordedSwitch("sns.denial.text", "AuthorizationError 403 with the grammar of 4.1, the action's prefix written SNS", "3.2", "IAM-ENF-03",
			authz.SessionUnreplayed, s.bindings.AuthorizedOps(snsapi.Service())),
		deliverySourceAccount(), subscribePolicyCheck(), defaultPolicyDirectCalls(s.bindings.AuthorizedOps(snsapi.Service())),
		authz.DeliveryResolveFirstSwitch("sns", "8.4", "IAM-ENF-22", s.bindings.DeliveryResolvedFirstOps(snsapi.Service())),
	}
	for _, sw := range httpSwitches() {
		entries = append(entries, sw.Entry())
	}
	return append(entries, filterpolicy.Switches()...)
}

// defaultPolicyDirectCalls is how the default topic policy, whose one
// statement is conditioned on AWS:SourceOwner, decides a direct call:
// understudy never supplies that key to a call made straight to SNS, so
// the statement does not match and a same-account caller needs an
// identity-policy allow (iam-enforcement-surfaces.md 7.2, IAM-ENF-17).
func defaultPolicyDirectCalls(ops []string) fidelity.Entry {
	return authz.RecordedSwitch("sns.default-policy.direct-calls", "identity-policy-required", "7.2", "IAM-ENF-17", authz.SessionUnreplayed, ops)
}

// deliverySourceAccount is whether SNS sends aws:SourceAccount, the topic's
// account, with each delivery to a queue or function, a confirmation
// included: present. AWS documents aws:SourceArn for SNS's deliveries and
// lists aws:SourceAccount only among the keys other services set when they
// publish (iam-enforcement-surfaces.md 8.1, IAM-ENF-18). topicRef.deliveryFrom
// applies it.
func deliverySourceAccount() fidelity.Entry {
	return authz.RecordedSwitch("sns.delivery.source-account", "present", "8.1", "IAM-ENF-18", authz.DeliveryUnreplayed,
		[]string{"sns.Publish", "sns.Subscribe"})
}

// subscribePolicyCheck is what Subscribe checks of an sqs or lambda
// endpoint's resource policy: none, so a policy that refuses SNS refuses
// each delivery instead, under iam.enforce=enforce
// (iam-enforcement-surfaces.md 8.2, IAM-ENF-19).
func subscribePolicyCheck() fidelity.Entry {
	return authz.RecordedSwitch("sns.subscribe.policy-check", "none", "8.2", "IAM-ENF-19", authz.DeliveryUnreplayed,
		[]string{"sns.Subscribe"})
}
