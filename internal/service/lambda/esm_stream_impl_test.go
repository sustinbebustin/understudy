package lambda_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// A retry limit, a record age and an on-failure destination are applied
// and echoed together, on Create and on an Update that adds either
// (recorded LS-D:L304-L457 creates MaximumRetryAttempts 1 with an SQS
// destination; lambda-stream-sources.md 3.5, 5.6).
func TestStreamMappingDestinationBesideLimits(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.createFunction("worker")
		dlq := "arn:aws:sqs:us-east-1:000000000000:dlq"
		withDLQ := &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(dlq)}}
		m := call(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(lambdaapi.CreateEventSourceMappingInput{
			MaximumRetryAttempts: new(int32(3)), MaximumRecordAgeInSeconds: new(int32(120)), DestinationConfig: withDLQ,
		}))
		if derefAny(m.MaximumRetryAttempts) != int32(3) || derefAny(m.MaximumRecordAgeInSeconds) != int32(120) ||
			m.DestinationConfig == nil || m.DestinationConfig.OnFailure == nil || derefAny(m.DestinationConfig.OnFailure.Destination) != dlq {
			h.Errorf("Create echoed %v, %v, %+v; want 3, 120 and the destination", derefAny(m.MaximumRetryAttempts), derefAny(m.MaximumRecordAgeInSeconds), m.DestinationConfig)
		}
		h.settle()
		cleared := call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{
			UUID: *m.UUID, DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new("")}},
		})
		if cleared.DestinationConfig == nil || cleared.DestinationConfig.OnFailure == nil || cleared.DestinationConfig.OnFailure.Destination != nil {
			h.Errorf("Update clearing the destination = %+v, want {\"OnFailure\": {}}", cleared.DestinationConfig)
		}
		h.settle()
		up := call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{
			UUID: *m.UUID, MaximumRetryAttempts: new(int32(2)), DestinationConfig: withDLQ,
		})
		if derefAny(up.MaximumRetryAttempts) != int32(2) || up.DestinationConfig == nil || up.DestinationConfig.OnFailure == nil || derefAny(up.DestinationConfig.OnFailure.Destination) != dlq {
			h.Errorf("Update adding a limit and a destination = %v, %+v; want 2 and the destination", derefAny(up.MaximumRetryAttempts), up.DestinationConfig)
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
