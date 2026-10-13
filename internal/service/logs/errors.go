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
// startFromHead=false with a startTime before 2024 (inferred text).
func errBackwardBefore2024() *awserr.Error {
	return logsapi.ErrInvalidParameterException("startFromHead=false does not accept a startTime before 2024-01-01T00:00:00Z.")
}

// errDeletionProtected answers DeleteLogGroup on a protected group, in the
// text the user guide documents
// (https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/protecting-log-groups-from-deletion.html).
func errDeletionProtected() *awserr.Error {
	return logsapi.ErrValidationException("Cannot delete log group with deletion protection enabled. Disable deletion protection first.")
}

// errStreamDeletionProtected answers DeleteLogStream in a protected group,
// which deletion protection also covers; the ValidationException is the
// model's, the text is inferred from errDeletionProtected's.
func errStreamDeletionProtected() *awserr.Error {
	return logsapi.ErrValidationException("Cannot delete log stream in a log group with deletion protection enabled. Disable deletion protection first.")
}

// errIdentifiersAndFilters is DescribeLogGroups' documented rule that
// logGroupIdentifiers takes no other filter but includeLinkedAccounts
// (inferred text).
func errIdentifiersAndFilters() *awserr.Error {
	return logsapi.ErrInvalidParameterException("logGroupIdentifiers cannot be combined with logGroupNamePrefix, logGroupNamePattern or logGroupClass.")
}

// errInfrequentAccess refuses GetLogEvents and FilterLogEvents on an
// INFREQUENT_ACCESS group, which the log class matrix lists as not
// supported (inferred text).
func errInfrequentAccess(op string) *awserr.Error {
	return logsapi.ErrInvalidParameterException(op + " is not supported for log groups in the INFREQUENT_ACCESS log class. Use CloudWatch Logs Insights instead.")
}

// errFixedRetention refuses a retention change on a DELIVERY group, whose
// retention the log class documentation fixes (inferred text).
func errFixedRetention() *awserr.Error {
	return logsapi.ErrInvalidParameterException("The retention period of a log group in the DELIVERY log class is fixed and cannot be changed.")
}

// errKMSKey refuses a kmsKeyId that is not the ARN of an enabled symmetric
// encryption key. The text is the one third-party reports record for a
// bare key ID (third-party:
// https://github.com/hashicorp/terraform-provider-aws/issues/41606); that
// AWS sends it for a missing, disabled or asymmetric key too is inferred.
func errKMSKey() *awserr.Error {
	return logsapi.ErrInvalidParameterException("The specified KMS Key Id could not be found.")
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

// errTooManyTags is the 50-tag limit of a log group under TagResource
// (inferred text), with the modeled resourceName member naming the group's
// ARN.
func errTooManyTags(arn string) *awserr.Error {
	e := logsapi.ErrTooManyTagsException(tooManyTagsText(arn))
	e.Fields = append(e.Fields, awserr.Field{Name: "resourceName", Value: arn})
	return e
}

// errTooManyTagsLegacy is the same limit under TagLogGroup, whose model
// lists no TooManyTagsException; the code is its modeled client error, the
// text errTooManyTags' (inferred).
func errTooManyTagsLegacy(arn string) *awserr.Error {
	return logsapi.ErrInvalidParameterException(tooManyTagsText(arn))
}

func tooManyTagsText(arn string) string {
	return "A resource can have no more than " + strconv.Itoa(maxTags) + " tags: " + arn
}

func errFilterPattern(message string) *awserr.Error {
	return logsapi.ErrInvalidParameterException(message)
}
