package esm

import "github.com/sustinbebustin/understudy/internal/aws"

// EventSourceKind is the kind of source an event source mapping polls,
// with the settings only that kind has.
//
//sumtype:decl
type EventSourceKind interface{ isEventSourceKind() }

// SQS is an SQS queue; the mapping's source is the queue's ARN. Scaling
// lives here, so a stream mapping cannot hold one (spec conflict row 6;
// lambda-stream-sources.md 3.4).
type SQS struct {
	Scaling Scaling `json:"scaling"`
}

func (SQS) isEventSourceKind() {}

// SourceKindOf returns the kind of source an EventSourceArn names: SQS,
// unbounded, for an sqs ARN, and false for any other service. DynamoDB
// and Kinesis streams are not named here: their kind needs the mapping's
// StartingPosition, which the control plane reads.
func SourceKindOf(source aws.ARN) (EventSourceKind, bool) {
	if source.Service == "sqs" {
		return SQS{Scaling: Unbounded{}}, true
	}
	return nil, false
}

// Scaling is how an SQS mapping's poller concurrency is bounded: not at
// all, by ScalingConfig.MaximumConcurrency, or by ProvisionedPollerConfig,
// which the documentation makes mutually exclusive.
//
//sumtype:decl
type Scaling interface{ isScaling() }

// Unbounded is a mapping without either configuration.
type Unbounded struct{}

// MaximumConcurrency is ScalingConfig.MaximumConcurrency.
type MaximumConcurrency struct {
	N int32 `json:"n"`
}

// ProvisionedPollers is ProvisionedPollerConfig, stored and echoed.
type ProvisionedPollers struct {
	Minimum *int32 `json:"minimum,omitempty"`
	Maximum *int32 `json:"maximum,omitempty"`
	Group   string `json:"group,omitempty"`
}

func (Unbounded) isScaling()          {}
func (MaximumConcurrency) isScaling() {}
func (ProvisionedPollers) isScaling() {}
