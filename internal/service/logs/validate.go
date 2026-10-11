package logs

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// PutLogEvents batch limits (secrets-logs-events-alarms.md 3.3): the sum of
// the messages' UTF-8 bytes plus a fixed overhead per event.
const (
	maxBatchBytes = 1_048_576
	eventOverhead = 26
)

// validatePutRetentionPolicy checks retentionInDays against the values AWS
// accepts; the model has no constraint, so the generated validator cannot.
// The violation renders as the Coral enum text under
// InvalidParameterException.
func validatePutRetentionPolicy(in *logsapi.PutRetentionPolicyInput) []awserr.Violation {
	days := retentionDays()
	if slices.Contains(days, in.RetentionInDays) {
		return nil
	}
	values := make([]string, len(days))
	for i, d := range days {
		values[i] = strconv.Itoa(int(d))
	}
	return []awserr.Violation{{
		Path:       awserr.Path{"retentionInDays"},
		Value:      awserr.RenderedText{Text: strconv.Itoa(int(in.RetentionInDays))},
		Constraint: awserr.Enum{Values: values},
	}}
}

// checkCreateLogGroup refuses a name under the reserved "aws/" prefix,
// which the model documents but its pattern cannot express. A name like
// "/aws/lambda/fn" starts with "/" and stays valid.
func checkCreateLogGroup(in *logsapi.CreateLogGroupInput) *awserr.Error {
	if strings.HasPrefix(in.LogGroupName, "aws/") {
		return errReservedGroupName()
	}
	return nil
}

// checkPutLogEvents runs the batch rules that need no state: chronological
// order first, as AWS checks it before the time windows, then the batch
// size.
func checkPutLogEvents(in *logsapi.PutLogEventsInput) *awserr.Error {
	return checkBatch(in.LogEvents)
}

func checkPutLogEventsInternal(in *logsapi.PutLogEventsInternalInput) *awserr.Error {
	return checkBatch(in.LogEvents)
}

func checkBatch(events []logsapi.InputLogEvent) *awserr.Error {
	if !slices.IsSortedFunc(events, func(a, b logsapi.InputLogEvent) int { return cmp.Compare(a.Timestamp, b.Timestamp) }) {
		return errOutOfOrder()
	}
	size := 0
	for i := range events {
		size += len(events[i].Message) + eventOverhead
	}
	if size > maxBatchBytes {
		return errBatchTooLarge(size)
	}
	return nil
}

// checkDescribeLogStreams refuses logGroupName with logGroupIdentifier, and
// orderBy LastEventTime with a prefix.
func checkDescribeLogStreams(in *logsapi.DescribeLogStreamsInput) *awserr.Error {
	if in.LogGroupName != nil && in.LogGroupIdentifier != nil {
		return errNameAndIdentifier()
	}
	if in.OrderBy != nil && *in.OrderBy == logsapi.OrderByLastEventTime && in.LogStreamNamePrefix != nil {
		return errOrderByLastEventTime()
	}
	return nil
}

func checkGetLogEvents(in *logsapi.GetLogEventsInput) *awserr.Error {
	if in.LogGroupName != nil && in.LogGroupIdentifier != nil {
		return errNameAndIdentifier()
	}
	return nil
}

// backwardFloor is the earliest startTime FilterLogEvents accepts with
// startFromHead=false: 2024-01-01T00:00:00Z.
func backwardFloor() int64 { return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli() }

// checkFilterLogEvents runs FilterLogEvents' exclusivity rules.
func checkFilterLogEvents(in *logsapi.FilterLogEventsInput) *awserr.Error {
	switch {
	case in.LogGroupName != nil && in.LogGroupIdentifier != nil:
		return errNameAndIdentifier()
	case len(in.LogStreamNames) > 0 && in.LogStreamNamePrefix != nil:
		return errStreamNamesAndPrefix()
	case in.StartFromHead != nil && !*in.StartFromHead && (in.StartTime == nil || *in.StartTime < backwardFloor()):
		return errBackwardBefore2024()
	}
	return nil
}
