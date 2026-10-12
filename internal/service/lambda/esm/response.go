package esm

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
)

// Verdict is what an invocation's result says about the messages of its
// batch.
//
//sumtype:decl
type Verdict interface{ isVerdict() }

// Succeeded is a batch whose every message is processed and deleted.
type Succeeded struct{}

// Failed is a batch that failed as a whole: its messages return to the
// queue when their visibility timeout ends. Reason says why, for the log.
type Failed struct{ Reason string }

// PartiallyFailed is a batch whose messages IDs lists, in batch order,
// return to the queue while every other is deleted. IDs is never empty.
type PartiallyFailed struct{ IDs []string }

func (Succeeded) isVerdict()       {}
func (Failed) isVerdict()          {}
func (PartiallyFailed) isVerdict() {}

// ReadResponse reads a successful invocation's response under
// ReportBatchItemFailures (lambda-events.md 2.3). An empty or null
// response, an object without batchItemFailures, and a null or empty list
// are full success. Anything malformed fails the whole batch: a response
// that is not JSON or not an object, a list that is not a list, an entry
// that is not an object, and an itemIdentifier that is missing, not a
// string, empty or not the messageId of a message of batch. Keys match
// exactly.
func ReadResponse(payload []byte, batch []string) Verdict {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || bytes.Equal(payload, []byte("null")) {
		return Succeeded{}
	}
	if !json.Valid(payload) {
		return Failed{Reason: "the response is not valid JSON"}
	}
	var response map[string]json.RawMessage
	if json.Unmarshal(payload, &response) != nil {
		return Failed{Reason: "the response is not a JSON object"}
	}
	list, ok := response["batchItemFailures"]
	if !ok || bytes.Equal(list, []byte("null")) {
		return Succeeded{}
	}
	var entries []json.RawMessage
	if json.Unmarshal(list, &entries) != nil {
		return Failed{Reason: "batchItemFailures is not a list"}
	}
	inBatch := make(map[string]struct{}, len(batch))
	for _, id := range batch {
		inBatch[id] = struct{}{}
	}
	failed := map[string]bool{}
	for i, raw := range entries {
		id, reason := itemIdentifier(raw, "batchItemFailures["+strconv.Itoa(i)+"]")
		if _, ok := inBatch[id]; reason == "" && !ok {
			reason = "batchItemFailures[" + strconv.Itoa(i) + "].itemIdentifier " + strconv.Quote(id) + " is not a message of the batch"
		}
		if reason != "" {
			return Failed{Reason: reason}
		}
		failed[id] = true
	}
	if len(failed) == 0 {
		return Succeeded{}
	}
	return PartiallyFailed{IDs: slices.DeleteFunc(slices.Clone(batch), func(id string) bool { return !failed[id] })}
}

// itemIdentifier reads one batchItemFailures entry, at path, and returns
// its id, or why it is malformed.
func itemIdentifier(raw json.RawMessage, path string) (id, reason string) {
	var entry map[string]json.RawMessage
	if json.Unmarshal(raw, &entry) != nil || entry == nil {
		return "", path + " is not an object"
	}
	v, ok := entry["itemIdentifier"]
	if !ok {
		return "", path + " has no itemIdentifier"
	}
	if json.Unmarshal(v, &id) != nil || bytes.Equal(v, []byte("null")) {
		return "", path + ".itemIdentifier is not a string"
	}
	if id == "" {
		return "", path + ".itemIdentifier is empty"
	}
	return id, ""
}
