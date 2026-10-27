package lambda_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// A stream setting understudy does not build yet answers 501 rather than
// being stored without effect (invariant 3; spec stop-rule fallback
// "before A7"): StartingPosition LATEST and any setting other than the 3.5
// defaults, BatchSize and Enabled.
func TestStreamMappingUnbuiltSettingsAnswer501(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.createFunction("worker")
		for name, in := range map[string]lambdaapi.CreateEventSourceMappingInput{
			"LATEST":                  {StartingPosition: new(lambdaapi.EventSourcePosition("LATEST"))},
			"MaximumRetryAttempts":    {MaximumRetryAttempts: new(int32(3))},
			"MaximumRecordAge":        {MaximumRecordAgeInSeconds: new(int32(120))},
			"a batching window":       {MaximumBatchingWindowInSeconds: new(int32(5))},
			"FunctionResponseTypes":   {FunctionResponseTypes: []lambdaapi.FunctionResponseType{"ReportBatchItemFailures"}},
			"an on-failure SQS queue": {DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new("arn:aws:sqs:us-east-1:000000000000:dlq")}}},
		} {
			err := fail(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(in))
			if err.ID.Shape.Name() != "NotImplemented" || err.ID.Status != 501 {
				h.Errorf("%s: %s %d %q, want 501 NotImplemented", name, err.ID.Shape.Name(), err.ID.Status, err.Message)
			}
		}
		// The 3.5 defaults stated explicitly are accepted.
		m := call(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(lambdaapi.CreateEventSourceMappingInput{
			MaximumRetryAttempts: new(int32(-1)), MaximumRecordAgeInSeconds: new(int32(-1)), ParallelizationFactor: new(int32(1)),
			BisectBatchOnFunctionError: new(false), TumblingWindowInSeconds: new(int32(0)), MaximumBatchingWindowInSeconds: new(int32(0)),
			DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{}},
		}))
		h.settle()
		err := fail(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, MaximumRetryAttempts: new(int32(2))})
		if err.ID.Status != 501 {
			h.Errorf("Update of MaximumRetryAttempts = %d %q, want 501", err.ID.Status, err.Message)
		}
		up := call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, BatchSize: new(int32(7))})
		if derefAny(up.BatchSize) != int32(7) || derefAny(up.StartingPosition) != lambdaapi.EventSourcePosition("TRIM_HORIZON") {
			h.Errorf("Update of BatchSize = %v, %v; want 7 on the TRIM_HORIZON mapping", derefAny(up.BatchSize), derefAny(up.StartingPosition))
		}
	})
}

// A stream mapping is listed and deleted like any mapping, rendering its
// stream members (lambda-stream-sources.md 3.5).
func TestStreamMappingListAndDelete(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{BatchSize: new(int32(5))})
		h.settle()
		list := call(h.harness, lambdaapi.OpListEventSourceMappings, &lambdaapi.ListEventSourceMappingsInput{EventSourceArn: new(streamARN)})
		if !slices.ContainsFunc(list.EventSourceMappings, func(e lambdaapi.EventSourceMappingConfiguration) bool {
			return derefAny(e.UUID) == *m.UUID && derefAny(e.BatchSize) == int32(5) && derefAny(e.StartingPosition) == lambdaapi.EventSourcePosition("TRIM_HORIZON")
		}) {
			h.Errorf("ListEventSourceMappings = %+v, want the stream mapping with BatchSize 5", list.EventSourceMappings)
		}
		gone := call(h.harness, lambdaapi.OpDeleteEventSourceMapping, &lambdaapi.DeleteEventSourceMappingInput{UUID: *m.UUID})
		if derefAny(gone.EventSourceArn) != streamARN || derefAny(gone.StateTransitionReason) != "User action" {
			h.Errorf("Delete = %+v", gone)
		}
		h.settle()
		if err := fail(h.harness, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: *m.UUID}); err.ID.Status != 404 {
			h.Errorf("Get after Delete = %d %q, want 404", err.ID.Status, err.Message)
		}
	})
}
