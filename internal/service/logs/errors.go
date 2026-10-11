package logs

import (
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The message builders over the generated constructors. Texts are the
// recorded ones of secrets-logs-events-alarms.md 3 unless a comment says
// otherwise; the unrecorded ones are kept here so a recording changes one
// place.

func errGroupNotFound() *awserr.Error {
	return logsapi.ErrResourceNotFoundException("The specified log group does not exist.")
}

func errStreamNotFound() *awserr.Error {
	return logsapi.ErrResourceNotFoundException("The specified log stream does not exist.")
}

func errGroupExists() *awserr.Error {
	return logsapi.ErrResourceAlreadyExistsException("The specified log group already exists")
}

func errStreamExists() *awserr.Error {
	return logsapi.ErrResourceAlreadyExistsException("The specified log stream already exists")
}

func errInvalidToken() *awserr.Error {
	return logsapi.ErrInvalidParameterException("The specified nextToken is invalid.")
}

func errPrefixAndPattern() *awserr.Error {
	return logsapi.ErrInvalidParameterException("LogGroup name prefix and LogGroup name pattern are mutually exclusive parameters.")
}

// errNameAndIdentifier is recorded for DescribeLogStreams; GetLogEvents and
// FilterLogEvents send the same (inferred).
func errNameAndIdentifier() *awserr.Error {
	return logsapi.ErrValidationException("LogGroup name and LogGroup ARN are mutually exclusive parameters.")
}

// errNoGroup is a request naming neither logGroupName nor
// logGroupIdentifier; the text is understudy's own.
func errNoGroup() *awserr.Error {
	return logsapi.ErrInvalidParameterException("Either logGroupName or logGroupIdentifier must be specified.")
}

func errOutOfOrder() *awserr.Error {
	return logsapi.ErrInvalidParameterException("Log events in a single PutLogEvents request must be in chronological order.")
}

// errSpan is the 24-hour batch rule of the PutLogEvents documentation; the
// text is inferred, no recording carries it.
func errSpan() *awserr.Error {
	return logsapi.ErrInvalidParameterException("The batch of log events in a single PutLogEvents request cannot span more than 24 hours.")
}

// errBatchTooLarge carries the size rule's text as AWS agents report it;
// no recording in the research carries it (inferred).
func errBatchTooLarge(size int) *awserr.Error {
	return logsapi.ErrInvalidParameterException("Upload too large: " + strconv.Itoa(size) + " bytes exceeds limit of " + strconv.Itoa(maxBatchBytes))
}

// errOrderByLastEventTime is the DescribeLogStreams rule that orderBy
// LastEventTime takes no prefix (inferred text).
func errOrderByLastEventTime() *awserr.Error {
	return logsapi.ErrInvalidParameterException("Cannot order by LastEventTime with a logStreamNamePrefix.")
}

// errStreamNamesAndPrefix is FilterLogEvents' documented exclusivity
// (inferred text).
func errStreamNamesAndPrefix() *awserr.Error {
	return logsapi.ErrInvalidParameterException("logStreamNames and logStreamNamePrefix are mutually exclusive parameters.")
}

// errBackwardBefore2024 is FilterLogEvents' documented rule for
// startFromHead=false (inferred text).
func errBackwardBefore2024() *awserr.Error {
	return logsapi.ErrInvalidParameterException("startFromHead=false requires a startTime on or after 2024-01-01T00:00:00Z.")
}

// errDeletionProtected answers DeleteLogGroup on a protected group with
// the ValidationException the model lists for it (inferred text).
func errDeletionProtected(name string) *awserr.Error {
	return logsapi.ErrValidationException("Log group " + name + " has deletion protection enabled. Disable deletion protection before deleting the log group.")
}

// errReservedGroupName refuses a CreateLogGroup name under the reserved
// "aws/" prefix the model documents (inferred text).
func errReservedGroupName() *awserr.Error {
	return logsapi.ErrInvalidParameterException("Log group names can't start with the string aws/.")
}

// errGroupLimit is the per-region log group quota (inferred text).
func errGroupLimit() *awserr.Error {
	return logsapi.ErrLimitExceededException("Resource limit exceeded.")
}

// errTooManyTags is the 50-tag limit of a log group (inferred text).
func errTooManyTags(arn string) *awserr.Error {
	return logsapi.ErrTooManyTagsException("A resource can have no more than " + strconv.Itoa(maxTags) + " tags: " + arn)
}

func errFilterPattern(message string) *awserr.Error {
	return logsapi.ErrInvalidParameterException(message)
}
