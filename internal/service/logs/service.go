package logs

import (
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/logsfilter"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerRetention fires when the oldest event of a log group with a
// retention policy expires. The key's ID is the group's ARN.
const TimerRetention kernel.TimerKind = "logs.retention"

// Options are the CloudWatch Logs settings. There are none yet; the zero
// value is AWS's behavior.
type Options struct{}

// Service is Amazon CloudWatch Logs: one state cell per account and region.
type Service struct {
	k     *kernel.Kernel
	cells *store.Partitioned[aws.Scope, State]
	// retention is TimerRetention as registered with the kernel.
	retention kernel.HandledKind
	// filter holds the filter-pattern behaviors no recording settles.
	filter logsfilter.Options
}

// New binds the Logs operations understudy implements and registers the
// retention timer. Every other Logs operation stays unbound and answers 501.
func New(d kernel.ServiceDeps, _ Options) *Service {
	codec := jsonx.NewCodec()
	s := &Service{
		k:     d.Kernel,
		cells: store.NewPartitioned(d.Kernel.Gate(), store.Codec{Marshal: codec.Marshal, Unmarshal: codec.Unmarshal}, newState),
	}
	s.retention = d.Kernel.Timers().Handle(TimerRetention, s.onRetention)
	b := logsapi.NewBinder(d.Binder)
	s.bindGroups(b)
	s.bindStreams(b)
	s.bindEvents(b)
	declareActions(d.Binder)
	return s
}

// declareActions states the IAM actions the access record keeps besides
// each operation's primary one. PutLogEventsInternal, the internal
// operation Lambda writes function logs through as the execution role,
// has no generated list; it declares logs:PutLogEvents, the action AWS
// writes those logs with, so the role's policy carries it beside
// logs:CreateLogGroup and logs:CreateLogStream (AWSLambdaBasicExecutionRole).
// CreateLogGroup with tags needs logs:TagResource (CreateLogGroup,
// Permissions); logs:TagLogGroup, the deprecated tagging action, is never
// declared. GetLogEvents and FilterLogEvents with unmask set need
// logs:Unmask to read masked data unmasked (GetLogEvents, unmask). It
// runs after the bind sites, whose bindings it extends.
func declareActions(b *kernel.Binder) {
	kernel.Declare(b, logsapi.OpGetLogEvents, kernel.WithActions(func(_ *kernel.Call, in *logsapi.GetLogEventsInput) []registry.IAMAction {
		return unmasking(in.Unmask)
	}))
	kernel.Declare(b, logsapi.OpFilterLogEvents, kernel.WithActions(func(_ *kernel.Call, in *logsapi.FilterLogEventsInput) []registry.IAMAction {
		return unmasking(in.Unmask)
	}))
	kernel.Declare(b, logsapi.OpPutLogEventsInternal, kernel.WithActions(func(*kernel.Call, *logsapi.PutLogEventsInternalInput) []registry.IAMAction {
		return []registry.IAMAction{{Prefix: "logs", Name: "PutLogEvents"}}
	}))
	kernel.Declare(b, logsapi.OpCreateLogGroup, kernel.WithActions(func(_ *kernel.Call, in *logsapi.CreateLogGroupInput) []registry.IAMAction {
		if len(in.Tags) == 0 {
			return nil
		}
		return []registry.IAMAction{{Prefix: "logs", Name: "TagResource"}}
	}))
}

// unmasking is logs:Unmask for a read that asks for unmasked data.
func unmasking(unmask bool) []registry.IAMAction {
	if !unmask {
		return nil
	}
	return []registry.IAMAction{{Prefix: "logs", Name: "Unmask"}}
}

// expiryRule is how retention removes events: deterministically at
// timestamp plus the retention period on the emulator clock, where AWS
// deletes them lazily, typically within 72 hours after. The rule decides
// which events the reads return, which PutLogEvents refuses as expired and
// what a shorter PutRetentionPolicy drops at once.
func expiryRule() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    "logs.retention.expiry",
		Value:   "at-expiry",
		Label:   evidence.AWSDoc,
		Ref:     "docs/research/secrets-logs-events-alarms.md#32-retention",
		Ops:     []string{"logs.PutLogEvents", "logs.PutRetentionPolicy", "logs.GetLogEvents", "logs.FilterLogEvents"},
		Settles: fidelity.NoScenario{Reason: "AWS deletes expired events lazily, typically within 72 hours, so no recording pins the instant"},
	}
}

// deliveryRetention is the fixed retention, in days, of a DELIVERY-class
// group. The log class guide says two days and that it cannot be changed;
// the model's logGroupClass documentation says one. Two is what the
// Terraform provider's acceptance tests read back from AWS.
func deliveryRetention() fidelity.Switch[int32] {
	return fidelity.Switch[int32]{
		Name:    "logs.delivery.retention-days",
		Value:   2,
		Label:   evidence.AWSDoc,
		Ref:     "https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/CloudWatch_Logs_Log_Classes.html",
		Ops:     []string{"logs.CreateLogGroup", "logs.DescribeLogGroups"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: no recording creates a DELIVERY-class log group"},
	}
}

// Switches lists the behaviors chosen without AWS-recorded evidence: the
// filter-pattern questions logsfilter leaves open, with the label,
// operations and settling step filterSwitches states for each, the
// retention rule and the DELIVERY class's fixed retention.
func (s *Service) Switches() []fidelity.Entry {
	switches := s.filter.Switches()
	table := filterSwitches()
	out := make([]fidelity.Entry, 0, 2+len(switches))
	out = append(out, expiryRule().Entry(), deliveryRetention().Entry())
	for _, sw := range switches {
		m := table[sw.Name]
		out = append(out, fidelity.Switch[string]{
			Name: sw.Name, Value: sw.Value, Label: m.label, Ref: sw.Ref, Record: sw.Record, Ops: m.ops, Settles: m.settles,
		}.Entry())
	}
	return out
}

// filterSwitch is what Logs states about one of logsfilter's switches:
// what backs its value, the operations it governs and what settles it.
type filterSwitch struct {
	label   evidence.Label
	ops     []string
	settles fidelity.Settles
}

// filterSwitches holds the filterSwitch of every logsfilter switch by
// name. A name it lacks gets no label, operations or settles, which the
// coverage checks refuse. FilterLogEvents is the one bound operation that
// takes a filter pattern.
func filterSwitches() map[string]filterSwitch {
	ops := []string{"logs.FilterLogEvents"}
	r10 := func(step string) fidelity.Settles {
		return fidelity.NoScenario{Reason: "logs/R10/" + step + ".json is recorded; no scenario replays logs/R10 yet"}
	}
	return map[string]filterSwitch{
		"logs.filter.term-boundary":         {evidence.Inferred, ops, r10("03-filter-01")},
		"logs.filter.delimited-field-count": {evidence.Inferred, ops, r10("03-filter-11")},
	}
}
