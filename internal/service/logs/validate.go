package logs

import (
	"cmp"
	"slices"
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

// checkPutRetentionPolicy checks retentionInDays against the values AWS
// accepts; the model has no constraint, so the generated validator cannot.
func checkPutRetentionPolicy(in *logsapi.PutRetentionPolicyInput) *awserr.Error {
	days := retentionDays()
	if slices.Contains(days, in.RetentionInDays) {
		return nil
	}
	return errRetentionValue(days)
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

// checkDescribeLogGroups runs DescribeLogGroups' exclusivity rules:
// prefix and pattern exclude each other, and logGroupIdentifiers admits
// no other filter but includeLinkedAccounts.
func checkDescribeLogGroups(in *logsapi.DescribeLogGroupsInput) *awserr.Error {
	switch {
	case in.LogGroupNamePrefix != nil && in.LogGroupNamePattern != nil:
		return errPrefixAndPattern()
	case len(in.LogGroupIdentifiers) > 0 && (in.LogGroupNamePrefix != nil || in.LogGroupNamePattern != nil || in.LogGroupClass != nil):
		return errIdentifiersAndFilters()
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

// checkFilterLogEvents runs FilterLogEvents' exclusivity rules. With
// startFromHead=false only a startTime that is sent and earlier than
// backwardFloor is refused; AWS's own "most recent events" example sends
// none.
func checkFilterLogEvents(in *logsapi.FilterLogEventsInput) *awserr.Error {
	switch {
	case in.LogGroupName != nil && in.LogGroupIdentifier != nil:
		return errNameAndIdentifier()
	case len(in.LogStreamNames) > 0 && in.LogStreamNamePrefix != nil:
		return errStreamNamesAndPrefix()
	case in.StartFromHead != nil && !*in.StartFromHead && in.StartTime != nil && *in.StartTime < backwardFloor():
		return errBackwardBefore2024()
	}
	return nil
}
