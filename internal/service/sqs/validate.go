package sqs

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// queueNamePattern is the overlay's QueueName pattern as AWS echoes it; a
// violation of it renders as the queue-name InvalidParameterValue text.
const queueNamePattern = `[a-zA-Z0-9_-]+(\.fifo)?`

// isSQSToken reports whether s is 1 to 80 letters, digits, hyphens and
// underscores: the rule SQS applies to batch entry ids, permission labels
// and queue names without their .fifo suffix (sqs.md 3, 5.3 and 6.2).
func isSQSToken(s string) bool {
	if len(s) < 1 || len(s) > 80 {
		return false
	}
	for i := range len(s) {
		b := s[i]
		if (b < '0' || b > '9') && (b < 'A' || b > 'Z') && (b < 'a' || b > 'z') && b != '-' && b != '_' {
			return false
		}
	}
	return true
}

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
