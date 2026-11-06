package events

import (
	"context"
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/events/envelope"
)

// bindServiceEvents binds PutServiceEventInternal, the way another
// service's own events reach EventBridge.
func (s *Service) bindServiceEvents(b eventbridgeapi.Binder) {
	b.PutServiceEventInternal(s.putServiceEvent, registry.Full{})
}

// putServiceEvent puts one AWS service event, such as a CloudWatch alarm
// state change, on the default bus of the call's scope, "which
// automatically receives events from AWS services"
// (cloudwatch-alarms.md 12.4). Rules match it as they match PutEvents
// events. The service is AWS, so its reserved aws. source is not refused.
// The envelope's time is the admission instant to the second, as AWS's
// examples show it (cloudwatch-alarms.md 6.2).
func (s *Service) putServiceEvent(ctx context.Context, c *kernel.Call, in *eventbridgeapi.PutServiceEventInternalInput) (*eventbridgeapi.PutServiceEventInternalOutput, *awserr.Error) {
	detail, failure := parseDetail(in.Detail)
	if failure != nil {
		return nil, eventbridgeapi.ErrInternalException("the " + in.Source + " service event " + in.DetailType +
			" carries a Detail that is not a JSON object EventBridge accepts, so it was not put on the default bus. This is a bug in understudy, please report it")
	}
	scope := c.Scope()
	ev := envelope.Event{
		ID:         s.ids.UUID(ids.KindEventID).String(),
		DetailType: in.DetailType,
		Source:     in.Source,
		Account:    scope.Account.String(),
		Region:     scope.Region.String(),
		Time:       c.Admitted().Wire().Truncate(time.Second),
		Resources:  slices.Clone(in.Resources),
		Detail:     detail,
	}
	c.Touch(busARN(scope, defaultBus))
	if err := s.publish(ctx, c, scope, defaultBus, []envelope.Event{ev}, false); err != nil {
		return nil, err
	}
	return &eventbridgeapi.PutServiceEventInternalOutput{}, nil
}
