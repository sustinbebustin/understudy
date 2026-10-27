package esm

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// Batch is the records of one stream invocation and the size of the event
// they make, counted over {"Records":[...]} as the function receives it
// against MaxPayload. Not implemented yet: it holds nothing.
type Batch[R any] struct {
	records []R
}

// Add appends r and reports true, or false when r would take the event
// past MaxPayload; the first record always fits. Not implemented yet: it
// answers false.
func (b *Batch[R]) Add(_ R) (bool, error) { return false, nil }

// Len returns the number of records.
func (b *Batch[R]) Len() int { return len(b.records) }

// Size returns the length of Event. Not implemented yet: it answers 0.
func (b *Batch[R]) Size() int { return 0 }

// Records returns the records in order. The caller must not change them.
func (b *Batch[R]) Records() []R { return b.records }

// Event returns the invocation's payload. Not implemented yet: it answers
// nil.
func (b *Batch[R]) Event() []byte { return nil }

// FailedBatch is what an on-failure record reports
// (lambda-stream-sources.md 5.6).
type FailedBatch struct {
	// RequestID is the last invocation's request id, or a new Lambda
	// request id for records discarded before any invocation.
	RequestID   string
	FunctionARN string
	Condition   FailCondition
	// InvokeCount is approximateInvokeCount.
	InvokeCount int
	// Timestamp is when the batch was discarded.
	Timestamp clock.Instant
	Stream    aws.ARN
	Shard     string
	// First and Last are the batch's first and last records; their Seq and
	// Arrival are read.
	First, Last StreamRecord
	// Size is batchSize.
	Size int
}

// FailCondition is why a batch was discarded.
//
//sumtype:decl
type FailCondition interface{ isFailCondition() }

// RetryAttemptsExhausted is a batch whose retries ran out; it carries the
// last invocation's response context.
type RetryAttemptsExhausted struct {
	StatusCode      int
	ExecutedVersion string
	// FunctionError is the X-Amz-Function-Error value; "" renders null, as
	// after a reported partial failure.
	FunctionError string
}

// RecordAgeExceeded is a record older than MaximumRecordAgeInSeconds; its
// record has no response context.
type RecordAgeExceeded struct{}

func (RetryAttemptsExhausted) isFailCondition() {}
func (RecordAgeExceeded) isFailCondition()      {}

// FailureRecord renders b as kind's on-failure record. Not implemented
// yet: it answers nil.
func FailureRecord(_ EventSourceKind, _ FailedBatch) []byte { return nil }
