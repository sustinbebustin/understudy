package logs

import (
	"context"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindStreams binds the log stream control plane.
func (s *Service) bindStreams(b logsapi.Binder) {
	b.CreateLogStream(s.createLogStream, registry.Full{},
		streamNamed(func(in *logsapi.CreateLogStreamInput) (string, string) { return in.LogGroupName, in.LogStreamName }))
	b.DeleteLogStream(s.deleteLogStream, registry.Full{},
		streamNamed(func(in *logsapi.DeleteLogStreamInput) (string, string) { return in.LogGroupName, in.LogStreamName }))
	b.DescribeLogStreams(s.describeLogStreams, registry.Full{},
		kernel.WithCheck(checkDescribeLogStreams),
		groupIdentified(
			func(in *logsapi.DescribeLogStreamsInput) (*string, *string) {
				return in.LogGroupName, in.LogGroupIdentifier
			},
			func(ref groupRef, _ *logsapi.DescribeLogStreamsInput) aws.ARN { return ref.arn() }))
}

// streamNamed names the stream an input addresses by group and stream name
// as the call's resource, before fault rules run.
func streamNamed[In any](namesOf func(*In) (group, stream string)) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		g, st := namesOf(in)
		return []aws.ARN{named(c, g).streamARN(st)}
	})
}

// createLogStream creates an empty stream in a group.
func (s *Service) createLogStream(_ context.Context, c *kernel.Call, in *logsapi.CreateLogStreamInput) (*logsapi.CreateLogStreamOutput, *awserr.Error) {
	ref := named(c, in.LogGroupName)
	err := s.updateGroup(c, ref, func(g *Group, now clock.Instant, _ *kernel.Tx) *awserr.Error {
		if _, ok := g.Streams[in.LogStreamName]; ok {
			return errStreamExists()
		}
		if g.Streams == nil {
			g.Streams = map[string]Stream{}
		}
		g.Streams[in.LogStreamName] = Stream{Name: in.LogStreamName, Created: now}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &logsapi.CreateLogStreamOutput{}, nil
}

// deleteLogStream deletes a stream with its events, unless the group's
// deletion protection, which covers its streams, is on.
func (s *Service) deleteLogStream(_ context.Context, c *kernel.Call, in *logsapi.DeleteLogStreamInput) (*logsapi.DeleteLogStreamOutput, *awserr.Error) {
	ref := named(c, in.LogGroupName)
	err := s.updateGroup(c, ref, func(g *Group, _ clock.Instant, tx *kernel.Tx) *awserr.Error {
		if g.DeletionProtection {
			return errStreamDeletionProtected()
		}
		if _, ok := g.Streams[in.LogStreamName]; !ok {
			return errStreamNotFound()
		}
		delete(g.Streams, in.LogStreamName)
		s.armExpiry(tx, ref, g)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &logsapi.DeleteLogStreamOutput{}, nil
}

// describeLogStreams lists a group's streams by name or by last event time.
func (s *Service) describeLogStreams(_ context.Context, c *kernel.Call, in *logsapi.DescribeLogStreamsInput) (*logsapi.DescribeLogStreamsOutput, *awserr.Error) {
	ref, err := resolveGroup(c, in.LogGroupName, in.LogGroupIdentifier)
	if err != nil {
		return nil, err
	}
	byTime := in.OrderBy != nil && *in.OrderBy == logsapi.OrderByLastEventTime
	items, ok := group(s, ref, func(g *Group) []keyed[logsapi.LogStream] {
		var out []keyed[logsapi.LogStream]
		for _, name := range g.streamNames() {
			st := g.Streams[name]
			if in.LogStreamNamePrefix != nil && !strings.HasPrefix(name, *in.LogStreamNamePrefix) {
				continue
			}
			key := name
			if byTime {
				var last int64
				if len(st.Events) > 0 {
					last = lastTimestamp(st.Events)
				}
				key = timeKey(last, name)
			}
			out = append(out, keyed[logsapi.LogStream]{key: key, item: describeStream(ref, &st)})
		}
		return out
	})
	if !ok {
		return nil, errGroupNotFound()
	}
	limit := defaultListLimit
	if in.Limit != nil {
		limit = int(*in.Limit)
	}
	page, next, ok := pageOf(items, streamListing(ref), in.Descending != nil && *in.Descending, in.NextToken, limit, c.Admitted())
	if !ok {
		return nil, errInvalidToken()
	}
	if page == nil {
		page = []logsapi.LogStream{}
	}
	return &logsapi.DescribeLogStreamsOutput{LogStreams: page, NextToken: next}, nil
}

// describeStream is the LogStream DescribeLogStreams returns: the event
// fields appear once the stream holds an event; storedBytes is always 0,
// as AWS documents for streams (secrets-logs-events-alarms.md 3.1).
func describeStream(ref groupRef, st *Stream) logsapi.LogStream {
	out := logsapi.LogStream{
		LogStreamName: new(st.Name),
		CreationTime:  new(millis(st.Created)),
		Arn:           new(ref.streamARN(st.Name).String()),
		StoredBytes:   new(int64(0)),
	}
	if len(st.Events) > 0 {
		out.FirstEventTimestamp = new(st.Events[0].Timestamp)
		out.LastEventTimestamp = new(lastTimestamp(st.Events))
	}
	if !st.LastIngestion.IsZero() {
		out.LastIngestionTime = new(millis(st.LastIngestion))
		out.UploadSequenceToken = new(sequenceToken(st.LastSeq))
	}
	return out
}

// lastTimestamp is the newest timestamp of events in position order.
func lastTimestamp(events []Event) int64 { return events[len(events)-1].Timestamp }
