package sqs

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// queueNamePattern is the overlay's QueueName pattern as AWS echoes it; a
// violation of it renders as the queue-name InvalidParameterValue text.
const queueNamePattern = `[a-zA-Z0-9_-]+(\.fifo)?`

// validateCreateQueue checks the queue name against the queue type, which
// the generated pattern cannot: a .fifo name needs FifoQueue=true and
// FifoQueue=true needs a .fifo name (sqs.md 3; both InvalidParameterValue,
// recorded by code only, so the queue-name text is reused).
func validateCreateQueue(in *sqsapi.CreateQueueInput) []awserr.Violation {
	suffixed := strings.HasSuffix(in.QueueName, ".fifo")
	if suffixed == strings.EqualFold(in.Attributes[sqsapi.QueueAttributeNameFifoQueue], "true") {
		return nil
	}
	return []awserr.Violation{{
		Path:       awserr.Path{"queueName"},
		Value:      awserr.RenderedText{Text: in.QueueName},
		Constraint: awserr.Pattern{Echo: queueNamePattern},
	}}
}
