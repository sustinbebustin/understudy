package logs

import (
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/logsfilter"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerRetention fires when the oldest event of a log group with a
// retention policy expires. The key's ID is the group's ARN.
const TimerRetention kernel.TimerKind = "logs.retention"

// Options are the CloudWatch Logs settings wire.go fills from the
// configuration. The zero value is AWS's behavior; there are no settings
// yet.
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
	return s
}

// expiryRule is how retention removes events: deterministically at
// timestamp plus the retention period on the emulator clock, where AWS
// deletes them lazily, typically within 72 hours after.
func expiryRule() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "logs.retention.expiry",
		Value: "at-expiry",
		Label: evidence.AWSDoc,
		Ref:   "docs/research/phase1/secrets-logs-events-alarms.md#32-retention",
	}
}

// Switches lists the behaviors chosen without AWS-recorded evidence: the
// filter-pattern questions logsfilter leaves open and the retention rule.
func (s *Service) Switches() []fidelity.Entry {
	switches := s.filter.Switches()
	out := make([]fidelity.Entry, 0, 1+len(switches))
	out = append(out, expiryRule().Entry())
	for _, sw := range switches {
		out = append(out, fidelity.Switch[string]{
			Name: sw.Name, Value: sw.Value, Label: evidence.Inferred, Ref: sw.Ref, Record: sw.Record,
		}.Entry())
	}
	return out
}
