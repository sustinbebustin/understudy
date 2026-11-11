package sfn

import (
	"math"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
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
// position: the id of the next event.
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

// integer is the integer member key; nil when it is absent or not an
// integer.
func (d details) integer(key string) *int64 {
	for _, e := range d.m {
		if n, ok := e.Value.(document.Number); ok && e.Key == key {
			if v, err := n.Int64(); err == nil {
				return &v
			}
		}
	}
	return nil
}

// count is the int32 member key, a Map's length or an iteration's index,
// which the engine writes below 2^31; 0 when it is absent.
func (d details) count(key string) int32 {
	n := d.integer(key)
	if n == nil || *n < 0 || *n > math.MaxInt32 {
		return 0
	}
	return int32(*n)
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
			Name: smithy.Deref(d.str("name")), Input: d.data("input", includeData), InputDetails: d.dataDetails("inputDetails"),
		}
	case strings.HasSuffix(r.Type, "StateExited"):
		out.StateExitedEventDetails = &sfnapi.StateExitedEventDetails{
			Name: smithy.Deref(d.str("name")), Output: d.data("output", includeData), OutputDetails: d.dataDetails("outputDetails"),
		}
	case r.Type == "MapStateStarted":
		out.MapStateStartedEventDetails = &sfnapi.MapStateStartedEventDetails{Length: d.count("length")}
	case strings.HasPrefix(r.Type, "MapIteration"):
		mapIterationDetails(&out, d)
	default:
		taskDetails(&out, d, includeData)
	}
	return out
}

// mapIterationDetails fills a MapIteration* event's {name, index}
// (stepfunctions-asl.md 11.3).
func mapIterationDetails(out *sfnapi.HistoryEvent, d details) {
	it := &sfnapi.MapIterationEventDetails{Name: d.str("name"), Index: d.count("index")}
	switch string(out.Type) {
	case "MapIterationStarted":
		out.MapIterationStartedEventDetails = it
	case "MapIterationSucceeded":
		out.MapIterationSucceededEventDetails = it
	case "MapIterationFailed":
		out.MapIterationFailedEventDetails = it
	case "MapIterationAborted":
		out.MapIterationAbortedEventDetails = it
	}
}

// taskDetails fills the details of a Task's events, in their lambda:invoke
// (Task*) and function ARN (LambdaFunction*) forms (stepfunctions-asl.md
// 11.3).
func taskDetails(out *sfnapi.HistoryEvent, d details, includeData bool) {
	switch string(out.Type) {
	case "TaskScheduled":
		out.TaskScheduledEventDetails = &sfnapi.TaskScheduledEventDetails{
			ResourceType: smithy.Deref(d.str("resourceType")), Resource: smithy.Deref(d.str("resource")), Region: smithy.Deref(d.str("region")),
			Parameters: smithy.Deref(d.str("parameters")), TimeoutInSeconds: d.integer("timeoutInSeconds"),
		}
	case "TaskStarted":
		out.TaskStartedEventDetails = &sfnapi.TaskStartedEventDetails{ResourceType: smithy.Deref(d.str("resourceType")), Resource: smithy.Deref(d.str("resource"))}
	case "TaskSucceeded":
		out.TaskSucceededEventDetails = &sfnapi.TaskSucceededEventDetails{
			ResourceType: smithy.Deref(d.str("resourceType")), Resource: smithy.Deref(d.str("resource")),
			Output: d.data("output", includeData), OutputDetails: d.dataDetails("outputDetails"),
		}
	case "TaskFailed":
		out.TaskFailedEventDetails = &sfnapi.TaskFailedEventDetails{
			ResourceType: smithy.Deref(d.str("resourceType")), Resource: smithy.Deref(d.str("resource")), Error: d.str("error"), Cause: d.str("cause"),
		}
	case "TaskTimedOut":
		out.TaskTimedOutEventDetails = &sfnapi.TaskTimedOutEventDetails{
			ResourceType: smithy.Deref(d.str("resourceType")), Resource: smithy.Deref(d.str("resource")), Error: d.str("error"), Cause: d.str("cause"),
		}
	case "LambdaFunctionScheduled":
		out.LambdaFunctionScheduledEventDetails = &sfnapi.LambdaFunctionScheduledEventDetails{
			Resource: smithy.Deref(d.str("resource")), Input: d.data("input", includeData), InputDetails: d.dataDetails("inputDetails"),
			TimeoutInSeconds: d.integer("timeoutInSeconds"),
		}
	case "LambdaFunctionSucceeded":
		out.LambdaFunctionSucceededEventDetails = &sfnapi.LambdaFunctionSucceededEventDetails{
			Output: d.data("output", includeData), OutputDetails: d.dataDetails("outputDetails"),
		}
	case "LambdaFunctionFailed":
		out.LambdaFunctionFailedEventDetails = &sfnapi.LambdaFunctionFailedEventDetails{Error: d.str("error"), Cause: d.str("cause")}
	case "LambdaFunctionTimedOut":
		out.LambdaFunctionTimedOutEventDetails = &sfnapi.LambdaFunctionTimedOutEventDetails{Error: d.str("error"), Cause: d.str("cause")}
	}
}
