package sqs

import (
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// maxBatchEntries is the most entries a batch request carries (sqs.md 6.2).
const maxBatchEntries = 10

// checkBatchIDs checks what fails a whole batch request about its entry ids
// (sqs.md 6.2): at least one entry and at most ten, each id 1 to 80
// alphanumeric characters, hyphens and underscores, and no id twice. entry
// is the entry type the EmptyBatchRequest text names.
func checkBatchIDs(entry string, entryIDs []string) *awserr.Error {
	switch {
	case len(entryIDs) == 0:
		return errEmptyBatch(entry)
	case len(entryIDs) > maxBatchEntries:
		return errTooManyEntries(len(entryIDs))
	}
	for _, id := range entryIDs {
		if !isSQSToken(id) {
			return errInvalidBatchEntryID()
		}
	}
	seen := make(map[string]bool, len(entryIDs))
	for _, id := range entryIDs {
		if seen[id] {
			return errBatchIDsNotDistinct(id)
		}
		seen[id] = true
	}
	return nil
}

// entryIDs collects the id of each batch entry, in entry order.
func entryIDs[E any](entries []E, id func(*E) string) []string {
	out := make([]string, len(entries))
	for i := range entries {
		out[i] = id(&entries[i])
	}
	return out
}

// batchFailure is the Failed entry of a batch entry err refused: the
// error's Query code and text, the sender's fault as every entry error is
// (sqs.md 6.2).
func batchFailure(id string, err *awserr.Error) sqsapi.BatchResultErrorEntry {
	return sqsapi.BatchResultErrorEntry{
		Id:          id,
		SenderFault: err.ID.Fault == awserr.Sender,
		Code:        err.ID.QueryCode,
		Message:     new(err.Message),
	}
}
