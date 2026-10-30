package scheduler

import (
	"context"
	"errors"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerFire is the timer of one schedule's next invocation, keyed by the
// schedule ARN and armed at the invocation's due instant: the scheduled
// instant plus the flexible-window offset (scheduler.md 14.2.5, 14.3.7).
const TimerFire kernel.TimerKind = "scheduler.fire"

// stateVersion is the schema version of the service's snapshot section.
const stateVersion = 1

// Service is Amazon EventBridge Scheduler: one state cell per account and
// region.
type Service struct {
	opts Options
}

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// New binds the 12 Scheduler operations (scheduler.md 2.2). Not implemented
// yet: every bound operation answers 501 NotImplemented.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{opts: o}
	b := schedulerapi.NewBinder(d.Binder)
	cov := registry.Partial{Note: "not implemented yet"}
	b.CreateSchedule(unbuilt[schedulerapi.CreateScheduleInput, schedulerapi.CreateScheduleOutput], cov)
	b.GetSchedule(unbuilt[schedulerapi.GetScheduleInput, schedulerapi.GetScheduleOutput], cov)
	b.UpdateSchedule(unbuilt[schedulerapi.UpdateScheduleInput, schedulerapi.UpdateScheduleOutput], cov)
	b.DeleteSchedule(unbuilt[schedulerapi.DeleteScheduleInput, schedulerapi.DeleteScheduleOutput], cov)
	b.ListSchedules(unbuilt[schedulerapi.ListSchedulesInput, schedulerapi.ListSchedulesOutput], cov)
	b.CreateScheduleGroup(unbuilt[schedulerapi.CreateScheduleGroupInput, schedulerapi.CreateScheduleGroupOutput], cov)
	b.GetScheduleGroup(unbuilt[schedulerapi.GetScheduleGroupInput, schedulerapi.GetScheduleGroupOutput], cov)
	b.ListScheduleGroups(unbuilt[schedulerapi.ListScheduleGroupsInput, schedulerapi.ListScheduleGroupsOutput], cov)
	b.DeleteScheduleGroup(unbuilt[schedulerapi.DeleteScheduleGroupInput, schedulerapi.DeleteScheduleGroupOutput], cov)
	b.TagResource(unbuilt[schedulerapi.TagResourceInput, schedulerapi.TagResourceOutput], cov)
	b.UntagResource(unbuilt[schedulerapi.UntagResourceInput, schedulerapi.UntagResourceOutput], cov)
	b.ListTagsForResource(unbuilt[schedulerapi.ListTagsForResourceInput, schedulerapi.ListTagsForResourceOutput], cov)
	return s
}

// unbuilt answers an operation the service binds but no ticket has built
// yet.
func unbuilt[In, Out any](context.Context, *kernel.Call, *In) (*Out, *awserr.Error) { //nolint:unparam // a kernel.Handler; built handlers return their output
	return nil, schedulerapi.ErrNotImplemented("not implemented yet")
}

// Name returns "scheduler".
func (*Service) Name() ref.ServiceID { return "scheduler" }

// SchemaVersion returns the version of the service's snapshot section.
func (*Service) SchemaVersion() int { return stateVersion }

// Export returns no section: the service holds no state yet.
func (*Service) Export() ([]store.Section, error) { return nil, nil }

// errStateUnbuilt is Import's answer to a section until the state exists.
var errStateUnbuilt = errors.New("scheduler: loading a snapshot section is not implemented yet; the section was not applied")

// Import accepts no section yet.
func (*Service) Import(ss []store.Section) error {
	if len(ss) > 0 {
		return errStateUnbuilt
	}
	return nil
}

// Reset has nothing to drop yet.
func (*Service) Reset(kernel.ResetScope) {}

// Rearm has no timers to arm yet.
func (*Service) Rearm(clock.Instant) {}

// Resources lists nothing yet.
func (*Service) Resources(aws.Scope) []kernel.Resource { return nil }

// Scopes returns no scope yet.
func (*Service) Scopes() []aws.Scope { return nil }
