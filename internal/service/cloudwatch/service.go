package cloudwatch

import (
	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Service is Amazon CloudWatch metrics and alarms: one state cell per
// account and region. Its handlers are written once and served through
// every protocol the registry lists for it: awsJson1_0, awsQuery and
// rpcv2Cbor.
type Service struct {
	k     *kernel.Kernel
	cells *store.Partitioned[aws.Scope, State]
}

// New binds the CloudWatch operations understudy implements. Every other
// operation stays unbound and answers 501 in CloudWatch's own error format.
func New(d kernel.ServiceDeps, _ Options) *Service {
	s := &Service{
		k:     d.Kernel,
		cells: store.NewPartitioned(d.Kernel.Gate(), stateCodec(), newState),
	}
	b := cloudwatchapi.NewBinder(d.Binder)
	s.bindAlarms(b)
	s.bindTags(b)
	s.bindMetrics(b)
	return s
}

// timestampWindow is which clock PutMetricData's two-week and two-hour
// windows read: a datum inside them by wall time or by emulator time is
// stored, as Logs does for PutLogEvents (lambda-runtime-contract.md 3),
// since an application stamps its data with wall time.
func timestampWindow() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "cloudwatch.put-metric-data.timestamp-window",
		Value: "wall-or-emulator",
		Label: evidence.Inferred,
		Ref:   "docs/research/phase1/secrets-logs-events-alarms.md#52-putmetricdata",
	}
}

// startRounding is how GetMetricData aligns its periods: StartTime rounds
// down to the minute, five minutes or hour by its age on the emulator
// clock, or to a sub-minute period's division of the minute, as AWS
// documents; no recording shows the period boundaries.
func startRounding() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "cloudwatch.get-metric-data.start-rounding",
		Value: "by-age",
		Label: evidence.AWSDoc,
		Ref:   "docs/research/phase1/secrets-logs-events-alarms.md#53-getmetricdata",
	}
}

// Switches lists the behaviors chosen without AWS-recorded evidence.
func (*Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{timestampWindow().Entry(), startRounding().Entry()}
}
