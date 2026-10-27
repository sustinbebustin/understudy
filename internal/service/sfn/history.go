package sfn

import (
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// docText is the compact JSON text of v, members in document order.
func docText(v document.Value) string {
	if v == nil {
		v = document.Null{}
	}
	e := jsonx.NewEncoder(jsonx.WireOrder)
	e.Value(v)
	b, err := e.Bytes()
	if err != nil {
		// Encoder.Value fails only for a nil value, which is null here.
		return "null"
	}
	return string(b)
}

// historyRecords are the stored form of the engine's events.
func historyRecords(events []asl.HistoryEvent) []HistoryRecord {
	out := make([]HistoryRecord, 0, len(events))
	for _, e := range events {
		r := HistoryRecord{ID: e.ID, Previous: e.PreviousEventID, Type: e.Type, Timestamp: e.Timestamp}
		if e.Details != nil {
			r.Details = docText(e.Details)
		}
		out = append(out, r)
	}
	return out
}

// positionText and positionOf write and read a history page token's
// position: the index of the next event.
func positionText(i int) string { return strconv.Itoa(i) }

func positionOf(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return i
}

// details reads the members of a stored event's details.
type details struct{ m document.Map }

func detailsOf(text string) details {
	if text == "" {
		return details{}
	}
	d := jsonx.NewDecoder([]byte(text))
	v, err := d.ReadValue()
	if err != nil {
		return details{}
	}
	if m, ok := v.(document.Map); ok {
		return details{m: m}
	}
	return details{}
}

// str is the string member key; nil when it is absent.
func (d details) str(key string) *string {
	for _, e := range d.m {
		if e.Key == key {
			if s, ok := e.Value.(document.String); ok {
				return new(string(s))
			}
		}
	}
	return nil
}

// data is a data member (input, output): nil when absent or when the
// caller asked for no execution data.
func (d details) data(key string, include bool) *string {
	if !include {
		return nil
	}
	return d.str(key)
}

// dataDetails is the HistoryEventExecutionDataDetails beside a data
// member.
func (d details) dataDetails(key string) *sfnapi.HistoryEventExecutionDataDetails {
	for _, e := range d.m {
		if e.Key != key {
			continue
		}
		out := &sfnapi.HistoryEventExecutionDataDetails{}
		m, ok := e.Value.(document.Map)
		if !ok {
			return out
		}
		for _, f := range m {
			if b, ok := f.Value.(document.Bool); ok && f.Key == "truncated" {
				out.Truncated = bool(b)
			}
		}
		return out
	}
	return nil
}

// wireEvent is the GetExecutionHistory form of a stored event: its
// <type>EventDetails member filled from the engine's wire-named details
// (stepfunctions-asl.md 11.3).
func wireEvent(r *HistoryRecord, includeData bool) sfnapi.HistoryEvent {
	out := sfnapi.HistoryEvent{
		Timestamp:       r.Timestamp,
		Type:            sfnapi.HistoryEventType(r.Type),
		Id:              r.ID,
		PreviousEventId: r.Previous,
	}
	d := detailsOf(r.Details)
	switch {
	case r.Type == "ExecutionStarted":
		out.ExecutionStartedEventDetails = &sfnapi.ExecutionStartedEventDetails{
			Input: d.data("input", includeData), InputDetails: d.dataDetails("inputDetails"), RoleArn: d.str("roleArn"),
		}
	case r.Type == "ExecutionSucceeded":
		out.ExecutionSucceededEventDetails = &sfnapi.ExecutionSucceededEventDetails{
			Output: d.data("output", includeData), OutputDetails: d.dataDetails("outputDetails"),
		}
	case r.Type == "ExecutionFailed":
		out.ExecutionFailedEventDetails = &sfnapi.ExecutionFailedEventDetails{Error: d.str("error"), Cause: d.str("cause")}
	case r.Type == "ExecutionAborted":
		out.ExecutionAbortedEventDetails = &sfnapi.ExecutionAbortedEventDetails{Error: d.str("error"), Cause: d.str("cause")}
	case r.Type == "ExecutionTimedOut":
		out.ExecutionTimedOutEventDetails = &sfnapi.ExecutionTimedOutEventDetails{Error: d.str("error"), Cause: d.str("cause")}
	case strings.HasSuffix(r.Type, "StateEntered"):
		out.StateEnteredEventDetails = &sfnapi.StateEnteredEventDetails{
			Name: deref(d.str("name")), Input: d.data("input", includeData), InputDetails: d.dataDetails("inputDetails"),
		}
	case strings.HasSuffix(r.Type, "StateExited"):
		out.StateExitedEventDetails = &sfnapi.StateExitedEventDetails{
			Name: deref(d.str("name")), Output: d.data("output", includeData), OutputDetails: d.dataDetails("outputDetails"),
		}
	}
	return out
}
