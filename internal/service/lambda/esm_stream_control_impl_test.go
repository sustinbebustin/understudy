package lambda_test

import (
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
)

// transitionsClock sets lambda.stateTransitions = clock, under which a
// mapping reports its transient states (ARCHITECTURE.md 9).
func transitionsClock(c *execConfig) { c.svc.StateTransitions = lambda.TransitionsClock }

// settleDelay is how long a transient state lasts under clock (service.go
// settleDelay), on the frozen test clock.
const settleDelay = 10 * time.Millisecond

// Under lambda.stateTransitions = final, the default, Create answers the
// settled Enabled and a disable Disabled, both with "User action"
// (ARCHITECTURE.md 9); TestStreamMappingCreateShape and
// TestStreamMappingUpdate assert AWS's transient states under clock
// (LS-D:L246-L271, LS-D:L173-L197).
func TestStreamMappingSettledStatesUnderFinal(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		if derefAny(m.State) != "Enabled" || derefAny(m.StateTransitionReason) != "User action" {
			h.Errorf("Create = %v, %v; want Enabled, User action", derefAny(m.State), derefAny(m.StateTransitionReason))
		}
		up := call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, Enabled: new(false)})
		if derefAny(up.State) != "Disabled" || derefAny(up.StateTransitionReason) != "User action" {
			h.Errorf("disable = %v, %v; want Disabled, User action", derefAny(up.State), derefAny(up.StateTransitionReason))
		}
	})
}

// Update checks a stream mapping's settings as Create does (contract 8.2
// "the stream-only 501s and record-age check apply"): an on-failure topic
// is echoed, an OnSuccess destination and a destination of another service
// answer the recorded unsupported text (12.4 item 9), a filter that does
// not compile answers 3.3 row 6, one that compiles and an S3 destination
// answer 501, and a record age of 30 the record-age text (ESMS-01). A
// refused update leaves the mapping as it was.
func TestStreamMappingUpdateChecksSettings(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.settle()
		topic := "arn:aws:sns:us-east-1:000000000000:failures"
		up := call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{
			UUID: *m.UUID, DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(topic)}},
		})
		if d := up.DestinationConfig; d == nil || d.OnFailure == nil || derefAny(d.OnFailure.Destination) != topic {
			h.Errorf("DestinationConfig = %+v, want OnFailure %s", d, topic)
		}
		h.settle()
		unsupported := "Unsupported DestinationConfig parameter for given event source mapping type."
		errorIs(h, fail(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{
			UUID: *m.UUID, DestinationConfig: &lambdaapi.DestinationConfig{OnSuccess: &lambdaapi.OnSuccess{Destination: new("arn:aws:sqs:us-east-1:000000000000:done")}},
		}), "InvalidParameterValueException", unsupported)
		errorIs(h, fail(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{
			UUID: *m.UUID, DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new("arn:aws:lambda:us-east-1:000000000000:function:other")}},
		}), "InvalidParameterValueException", unsupported)
		errorIs(h, fail(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{
			UUID: *m.UUID, FilterCriteria: &lambdaapi.FilterCriteria{Filters: []lambdaapi.Filter{{Pattern: new("single-string")}}},
		}), "InvalidParameterValueException", "Invalid filter pattern definition.")
		errorIs(h, fail(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{
			UUID: *m.UUID, MaximumRecordAgeInSeconds: new(int32(30)),
		}), "InvalidParameterValueException", "MaximumRecordAgeInSeconds must be -1 or between 60 and 604800.")
		for name, in := range map[string]lambdaapi.UpdateEventSourceMappingInput{
			"a filter that compiles":  {FilterCriteria: &lambdaapi.FilterCriteria{Filters: []lambdaapi.Filter{{Pattern: new(`{"eventName":["INSERT"]}`)}}}},
			"S3 on-failure":           {DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new("arn:aws:s3:::failures")}}},
			"ParallelizationFactor 2": {ParallelizationFactor: new(int32(2))},
		} {
			in.UUID = *m.UUID
			err := fail(h.harness, lambdaapi.OpUpdateEventSourceMapping, &in)
			if err.ID.Shape.Name() != "NotImplemented" || err.ID.Status != 501 {
				h.Errorf("%s: %s %d %q, want 501 NotImplemented", name, err.ID.Shape.Name(), err.ID.Status, err.Message)
			}
		}
		if d := h.get(*m.UUID).DestinationConfig; d == nil || d.OnFailure == nil || derefAny(d.OnFailure.Destination) != topic {
			h.Errorf("after refused updates DestinationConfig = %+v, want OnFailure %s kept", d, topic)
		}
	})
}

// Update moves a stream mapping to another function, checked against the
// stream as that function's execution role (contract 8.2 step 6), and
// refuses a function that does not exist with the existing text
// (lambda-events.md 1.3).
func TestStreamMappingUpdateMovesTheFunction(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.settle()
		other := h.createFunction("other")
		described := len(h.stream.callers("DescribeStream"))
		up := call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, FunctionName: new("other")})
		if derefAny(up.FunctionArn) != derefAny(other.FunctionArn) {
			h.Errorf("FunctionArn = %v, want %v", derefAny(up.FunctionArn), derefAny(other.FunctionArn))
		}
		if n := len(h.stream.callers("DescribeStream")); n != described+1 {
			h.Errorf("DescribeStream calls %d after the update, want %d", n, described+1)
		}
		h.settle()
		errorIs(h, fail(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, FunctionName: new("nope")}),
			"InvalidParameterValueException", "Function does not exist")
	})
}

// Under create-instant, LATEST on an empty stream anchors at TRIM_HORIZON:
// every record written after Create is delivered (ESMS-17).
func TestStreamLatestOnAnEmptyStream(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("LATEST"))})
		h.stream.add(h.Clock.Now(), "a", "b")
		h.start()
		if got, want := h.rec.ids(), [][]string{{"a", "b"}}; !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations %v, want %v", got, want)
		}
	})
}

// Under ReportBatchItemFailures a partial failure moves the checkpoint to
// just before the lowest reported record and resends from it, the records
// before it not again (lambda-stream-sources.md 5.5; with unlimited
// retries the resend repeats until it succeeds).
func TestStreamPartialFailureResendsFromTheLowestReported(t *testing.T) {
	runStream(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *streamHarness) {
		h.stream.add(h.Clock.Now(), "a", "b", "c")
		second := seqAt(1)
		failures := 1
		h.rec.setAnswer(func(streamEvent) runtimeapi.Outcome {
			if failures > 0 {
				failures--
				return runtimeapi.Success{Payload: []byte(`{"batchItemFailures":[{"itemIdentifier":"` + second + `"}]}`)}
			}
			return runtimeapi.Success{Payload: []byte("null")}
		})
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{FunctionResponseTypes: []lambdaapi.FunctionResponseType{"ReportBatchItemFailures"}})
		h.start()
		if got, want := h.rec.ids(), [][]string{{"a", "b", "c"}, {"b", "c"}}; !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations %v, want %v", got, want)
		}
		h.stream.add(h.Clock.Now(), "d")
		h.quiesce()
		if got := h.rec.ids(); !slices.Equal(got[len(got)-1], []string{"d"}) {
			h.Errorf("next invocation %v, want [d]", got[len(got)-1])
		}
		if got := derefAny(h.get(*m.UUID).LastProcessingResult); got != "OK" {
			h.Errorf("LastProcessingResult = %v, want OK", got)
		}
	})
}

// Without ReportBatchItemFailures the response is not read: a reported
// failure is a success (lambda-stream-sources.md 5.5).
func TestStreamResponseIgnoredWithoutReportBatchItemFailures(t *testing.T) {
	runStream(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *streamHarness) {
		h.stream.add(h.Clock.Now(), "a", "b")
		h.rec.setAnswer(func(streamEvent) runtimeapi.Outcome {
			return runtimeapi.Success{Payload: []byte(`{"batchItemFailures":[{"itemIdentifier":"` + seqAt(0) + `"}]}`)}
		})
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.start()
		if got, want := h.rec.ids(), [][]string{{"a", "b"}}; !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations %v, want %v", got, want)
		}
	})
}
