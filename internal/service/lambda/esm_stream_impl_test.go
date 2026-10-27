package lambda_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// A retry limit and a record age are applied and echoed; an on-failure
// destination on a mapping either can discard with answers 501 rather
// than being stored without effect, as on-failure records are not sent
// yet (invariant 3; spec stop-rule fallback "before A10"). The 3.5
// defaults stated explicitly are accepted with a destination.
func TestStreamMappingDiscardingDestinationAnswers501(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.createFunction("worker")
		dlq := "arn:aws:sqs:us-east-1:000000000000:dlq"
		withDLQ := &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(dlq)}}
		for name, in := range map[string]lambdaapi.CreateEventSourceMappingInput{
			"MaximumRetryAttempts": {MaximumRetryAttempts: new(int32(3)), DestinationConfig: withDLQ},
			"MaximumRecordAge":     {MaximumRecordAgeInSeconds: new(int32(120)), DestinationConfig: withDLQ},
		} {
			err := fail(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(in))
			if err.ID.Shape.Name() != "NotImplemented" || err.ID.Status != 501 {
				h.Errorf("%s with a destination: %s %d %q, want 501 NotImplemented", name, err.ID.Shape.Name(), err.ID.Status, err.Message)
			}
		}
		limited := call(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(lambdaapi.CreateEventSourceMappingInput{
			MaximumRetryAttempts: new(int32(3)), MaximumRecordAgeInSeconds: new(int32(120)),
		}))
		if derefAny(limited.MaximumRetryAttempts) != int32(3) || derefAny(limited.MaximumRecordAgeInSeconds) != int32(120) {
			h.Errorf("limits echoed as %v, %v; want 3, 120", derefAny(limited.MaximumRetryAttempts), derefAny(limited.MaximumRecordAgeInSeconds))
		}
		h.settle()
		if err := fail(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *limited.UUID, DestinationConfig: withDLQ}); err.ID.Status != 501 {
			h.Errorf("Update adding a destination to a limited mapping = %d %q, want 501", err.ID.Status, err.Message)
		}
		lifted := call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{
			UUID: *limited.UUID, MaximumRetryAttempts: new(int32(-1)), MaximumRecordAgeInSeconds: new(int32(-1)),
		})
		if derefAny(lifted.MaximumRetryAttempts) != int32(-1) || derefAny(lifted.MaximumRecordAgeInSeconds) != int32(-1) {
			h.Errorf("lifted limits echoed as %v, %v; want -1, -1", derefAny(lifted.MaximumRetryAttempts), derefAny(lifted.MaximumRecordAgeInSeconds))
		}
		call(h.harness, lambdaapi.OpDeleteEventSourceMapping, &lambdaapi.DeleteEventSourceMappingInput{UUID: *limited.UUID})
		h.settle()
		m := call(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(lambdaapi.CreateEventSourceMappingInput{
			MaximumRetryAttempts: new(int32(-1)), MaximumRecordAgeInSeconds: new(int32(-1)), ParallelizationFactor: new(int32(1)),
			BisectBatchOnFunctionError: new(false), TumblingWindowInSeconds: new(int32(0)), MaximumBatchingWindowInSeconds: new(int32(0)),
			DestinationConfig: withDLQ,
		}))
		h.settle()
		if err := fail(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, MaximumRetryAttempts: new(int32(2))}); err.ID.Status != 501 {
			h.Errorf("Update adding a retry limit beside a destination = %d %q, want 501", err.ID.Status, err.Message)
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
