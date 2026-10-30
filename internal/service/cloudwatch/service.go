package cloudwatch

import (
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerAlarmEvaluation is the kind of an evaluable alarm's evaluation
// timer, one per alarm, its id the alarm ARN (cloudwatch-alarms.md 12.3).
const TimerAlarmEvaluation kernel.TimerKind = "cloudwatch.alarm-evaluation"

// Service is Amazon CloudWatch metrics and alarms: one state cell per
// account and region. Its handlers are written once and served through
// every protocol the registry lists for it: awsJson1_0, awsQuery and
// rpcv2Cbor.
type Service struct {
	k     *kernel.Kernel
	cells *store.Partitioned[aws.Scope, State]
	// history is the alarm history per account and region, apart from
	// cells because it outlives the alarms (cloudwatch-alarms.md 8.1).
	history *store.Partitioned[aws.Scope, History]
	// evaluation is TimerAlarmEvaluation as registered with the kernel.
	evaluation kernel.HandledKind
}

// New binds the CloudWatch operations understudy implements and registers
// the alarm evaluation timer. Every other operation stays unbound and
// answers 501 in CloudWatch's own error format.
func New(d kernel.ServiceDeps, _ Options) *Service {
	s := &Service{
		k:       d.Kernel,
		cells:   store.NewPartitioned(d.Kernel.Gate(), stateCodec(), newState),
		history: store.NewPartitioned(d.Kernel.Gate(), stateCodec(), newHistory),
	}
	s.evaluation = d.Kernel.Timers().Handle(TimerAlarmEvaluation, s.onEvaluation)
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
		Name:    "cloudwatch.put-metric-data.timestamp-window",
		Value:   "wall-or-emulator",
		Label:   evidence.Inferred,
		Ref:     "docs/research/secrets-logs-events-alarms.md#52-putmetricdata",
		Ops:     []string{"cloudwatch.PutMetricData"},
		Settles: fidelity.NoScenario{Reason: "AWS has one clock: which of understudy's two the windows read is understudy's choice, which no recording settles"},
	}
}

// startRounding is how GetMetricData and GetMetricStatistics align their
// periods: StartTime rounds down to the minute, five minutes or hour by its
// age on the emulator clock, or to a sub-minute period's division of the
// minute, as AWS documents (cloudwatch-alarms.md 9.1, 12.2); no recording
// shows the period boundaries.
func startRounding() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    "cloudwatch.get-metric-data.start-rounding",
		Value:   "by-age",
		Label:   evidence.AWSDoc,
		Ref:     "docs/research/secrets-logs-events-alarms.md#53-getmetricdata",
		Ops:     []string{"cloudwatch.GetMetricData", "cloudwatch.GetMetricStatistics"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: no recording shows GetMetricData's period boundaries"},
	}
}

// Switches lists the behaviors chosen without AWS-recorded evidence, with
// the identity of a denial per protocol (iam-enforcement-surfaces.md 12.8).
func (s *Service) Switches() []fidelity.Entry {
	return append([]fidelity.Entry{
		timestampWindow().Entry(), startRounding().Entry(),
		authz.DenialSwitch("cloudwatch.denial.identity",
			"AccessDenied 403 over Query; __type com.amazon.coral.service#AccessDeniedException with header AccessDenied;Sender over JSON and CBOR",
			evidence.Inferred, "3.2", "IAM-ENF-09", s.k.Bindings().AuthorizedOps(cloudwatchapi.Service())),
	}, slices.Concat(alarmSwitches(), statisticsSwitches())...)
}
