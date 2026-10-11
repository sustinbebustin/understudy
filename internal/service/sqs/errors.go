package sqs

import (
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The message builders over the generated constructors. Texts are the
// recorded ones of sqs.md 2.4 unless a comment says otherwise; "inferred"
// marks a text no source records, written in the frame of a recorded
// sibling.

func errQueueDoesNotExist() *awserr.Error {
	return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
}

func errQueueExists(attr sqsapi.QueueAttributeName) *awserr.Error {
	return sqsapi.ErrQueueNameExists("A queue already exists with the same name and a different value for attribute " + string(attr))
}

func errQueueDeletedRecently() *awserr.Error {
	return sqsapi.ErrQueueDeletedRecently("You must wait 60 seconds after deleting a queue before you can create another with the same name.")
}

func errUnknownAttribute(name string) *awserr.Error {
	return sqsapi.ErrInvalidAttributeName("Unknown Attribute " + name + ".")
}

// errFifoOnlyAttribute is recorded for DeduplicationScope and
// FifoThroughputLimit; ContentBasedDeduplication follows the same text
// (inferred).
func errFifoOnlyAttribute(name sqsapi.QueueAttributeName) *awserr.Error {
	return sqsapi.ErrInvalidAttributeName("You can specify the " + string(name) + " only when FifoQueue is set to true.")
}

func errInvalidAttributeValue(name sqsapi.QueueAttributeName) *awserr.Error {
	return sqsapi.ErrInvalidAttributeValue("Invalid value for the parameter " + string(name) + ".")
}

// errPolicyTooLarge and errPolicyStatements refuse a Policy that
// CreateQueue or SetQueueAttributes would set over the documented size or
// statement limit: the recorded invalid-value text with a reason that says
// which limit (inferred: no source records what AWS answers there).
// Terraform retries any error containing "Invalid value for the parameter
// Policy" for Timeout/2, about 10 minutes, before it fails (tf-messaging.md
// 4.2 #18), so the reason is what tells its user what to fix once it does.
// AddPermission answers the same limits with errPermissionLimit.
func errPolicyTooLarge(size int) *awserr.Error {
	return errPolicyLimit("the policy is " + strconv.Itoa(size) + " bytes; at most " + strconv.Itoa(maxPolicyBytes) + " are allowed.")
}

func errPolicyStatements(n int) *awserr.Error {
	return errPolicyLimit("the policy has " + strconv.Itoa(n) + " statements; at most " + strconv.Itoa(maxPolicyStatements) + " are allowed.")
}

func errPolicyLimit(reason string) *awserr.Error {
	return sqsapi.ErrInvalidAttributeValue("Invalid value for the parameter " + string(sqsapi.QueueAttributeNamePolicy) + ". Reason: " + reason)
}

func errModifyQueueType() *awserr.Error {
	return sqsapi.ErrInvalidAttributeValue("Invalid value for the parameter FifoQueue. Reason: Modifying queue type is not supported.")
}

// errRedriveParameterMissing and errMaxReceiveCount carry LocalStack's
// texts: AWS's code, InvalidParameterValue, is recorded, its texts are not
// (sqs.md 5.1 and 9.1).
func errRedriveParameterMissing(key string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("The required parameter '" + key + "' is missing")
}

func errMaxReceiveCount(policy, count string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + policy + " for parameter RedrivePolicy is invalid. Reason: Invalid value for maxReceiveCount: " + count + ", valid values are from 1 to 1000 both inclusive.")
}

// errRedriveTarget refuses a redrive policy whose target the state does
// not admit (inferred).
func errRedriveTarget(policy, reason string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + policy + " for parameter RedrivePolicy is invalid. Reason: " + reason)
}

func errTwoEncryptionTypes() *awserr.Error {
	return sqsapi.ErrInvalidAttributeName("You can use one type of server-side encryption (SSE) at one time. You can either enable KMS SSE or SQS SSE.")
}

func errMessageTooLong(maxBytes int32) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("One or more parameters are invalid. Reason: Message must be shorter than " + strconv.Itoa(int(maxBytes)) + " bytes.")
}

func errBatchEntryTooLong(maxBytes int32) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("One or more parameters cannot be validated. Reason: Message must be shorter than " + strconv.Itoa(int(maxBytes)) + " bytes.")
}

// errInvalidMessageContents carries LocalStack's text; AWS's is unrecorded
// (sqs.md 6.1).
func errInvalidMessageContents() *awserr.Error {
	return sqsapi.ErrInvalidMessageContents("Invalid characters found. Valid unicode characters are #x9 | #xA | #xD | #x20 to #xD7FF | #xE000 to #xFFFD | #x10000 to #x10FFFF")
}

func errEmptyAttributeValue(name, dataType string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Message (user) attribute '" + name + "' must contain a non-empty value of type '" + dataType + "'.")
}

// The message and system attribute texts below are unrecorded: only the
// InvalidParameterValue code is (sqs.md 6.1). Each says whether it is
// moto's or inferred.

// errAttributeDataType is moto's text (moto/sqs/utils.py,
// parse_message_attributes).
func errAttributeDataType(name string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("The message attribute '" + name + "' has an invalid message attribute type, the set of supported type prefixes is Binary, Number, and String.")
}

// errTooManyAttributes states the documented cap of 10 (inferred).
func errTooManyAttributes(n int) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Number of message attributes [" + strconv.Itoa(n) + "] exceeds the allowed maximum [10].")
}

// errAttributeName is moto's text (moto/sqs/models.py,
// Message.validate_attribute_name).
func errAttributeName(name string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("The message attribute name '" + name + "' is invalid. Attribute name can contain A-Z, a-z, 0-9, underscore (_), hyphen (-), and period (.) characters.")
}

// errAttributeNumber frames the documented Number range in the recorded
// "Value X for parameter P is invalid" form (inferred).
func errAttributeNumber(name, value string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + value + " for parameter MessageAttribute " + name + " is invalid. Reason: The value is not a number of up to 38 digits between -10^126 and 10^126.")
}

// errAttributeString follows the recorded errEmptyAttributeValue frame
// with the body's character rule (inferred).
func errAttributeString(name string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Message (user) attribute '" + name + "' contains characters that are not valid. Valid unicode characters are #x9 | #xA | #xD | #x20 to #xD7FF | #xE000 to #xFFFD | #x10000 to #x10FFFF")
}

// errTraceHeader states the documented AWSTraceHeader type rule (inferred).
func errTraceHeader() *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value for parameter MessageSystemAttribute AWSTraceHeader is invalid. Reason: AWSTraceHeader must be a non-empty value of type String.")
}

// errSystemAttributeName refuses a system attribute other than
// AWSTraceHeader, the only one a send may carry (inferred).
func errSystemAttributeName(name string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + name + " for parameter MessageSystemAttributeName is invalid. Reason: AWSTraceHeader is the only system attribute a message can be sent with.")
}

// errVisibilityTimeout frames the documented range in the recorded
// "Value X for parameter P is invalid" form; the exact text is unrecorded.
func errVisibilityTimeout(v int32) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + strconv.Itoa(int(v)) + " for parameter VisibilityTimeout is invalid. Reason: Must be >= 0 and <= 43200, if provided.")
}

// errVisibilityCeiling is moto's text for the 12-hour ceiling (sqs.md 7.2).
func errVisibilityCeiling(v int32) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + strconv.Itoa(int(v)) + " for parameter VisibilityTimeout is invalid. Reason: Total VisibilityTimeout for the message is beyond the limit [43200 seconds]")
}

func errDelaySeconds(v int32) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + strconv.Itoa(int(v)) + " for parameter DelaySeconds is invalid. Reason: Must be >= 0 and <= 900, if provided.")
}

// errInvalidAttemptID applies the MessageGroupId text to
// ReceiveRequestAttemptId, which the documentation gives the same rule
// (inferred, sqs.md 8.3).
func errInvalidAttemptID(id string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + id + " for parameter ReceiveRequestAttemptId is invalid. Reason: ReceiveRequestAttemptId can only include alphanumeric and punctuation characters. 1 to 128 in length.")
}

// errMaxResults and errMoveRate frame the documented ranges as the
// recorded MaxNumberOfMessages text does (inferred).
func errMaxResults(n int32, maxResults int) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + strconv.Itoa(int(n)) + " for parameter MaxResults is invalid. Reason: Must be between 1 and " + strconv.Itoa(maxResults) + ", if provided.")
}

func errMoveRate(n int32) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + strconv.Itoa(int(n)) + " for parameter MaxNumberOfMessagesPerSecond is invalid. Reason: Must be between 1 and 500, if provided.")
}

// errInvalidLabel reuses the queue-name reason, the same character rule
// (inferred, sqs.md 5.3).
func errInvalidLabel(label string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + label + " for parameter Label is invalid. Reason: Can only include alphanumeric characters, hyphens, or underscores. 1 to 80 in length.")
}

// errInvalidAccountID refuses an AWSAccountIds entry that is not an account
// id (inferred).
func errInvalidAccountID(id string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + id + " for parameter AWSAccountId is invalid. Reason: An account id is 12 digits.")
}

func errInvalidGroupID(id string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + id + " for parameter MessageGroupId is invalid. Reason: MessageGroupId can only include alphanumeric and punctuation characters. 1 to 128 in length.")
}

func errInvalidDedupID(id string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + id + " for parameter MessageDeduplicationId is invalid. Reason: MessageDeduplicationId can only include alphanumeric and punctuation characters. 1 to 128 in length.")
}

func errMissingBody() *awserr.Error {
	return sqsapi.ErrMissingParameter("The request must contain the parameter MessageBody.")
}

func errMissingGroupID() *awserr.Error {
	return sqsapi.ErrMissingParameter("The request must contain the parameter MessageGroupId.")
}

// errBatchMissingGroupID is the batch form, recorded under another code.
func errBatchMissingGroupID() *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("The request must contain the parameter MessageGroupId.")
}

func errDedupRequired() *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("The queue should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly")
}

// errNotForQueueType is the recorded FIFO DelaySeconds text, sic; it is
// reused for MessageDeduplicationId on a standard queue (inferred).
func errNotForQueueType(param, value string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + value + " for parameter " + param + " is invalid. Reason: The request include parameter that is not valid for this queue type.")
}

func errReceiptHandleIsInvalid(handle string) *awserr.Error {
	return sqsapi.ErrReceiptHandleIsInvalid(`The input receipt handle "` + handle + `" is not a valid receipt handle.`)
}

func errReceiptHandleExpired(handle string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + handle + " for parameter ReceiptHandle is invalid. Reason: The receipt handle has expired.")
}

// errNotAvailable is ChangeMessageVisibility of a message that is gone or
// was received again; errEntryNotAvailable is the batch entry's form
// (sqs.md 6.2 and 7.2).
func errNotAvailable(handle string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + handle + " for parameter ReceiptHandle is invalid. Reason: Message does not exist or is not available for visibility timeout change.")
}

func errEntryNotAvailable() *awserr.Error {
	return sqsapi.ErrReceiptHandleIsInvalid("Message does not exist or is not available for visibility timeout change")
}

// errEmptyBatch is moto's text; the code is recorded.
func errEmptyBatch(entry string) *awserr.Error {
	return sqsapi.ErrEmptyBatchRequest("There should be at least one " + entry + " in the request.")
}

func errTooManyEntries(n int) *awserr.Error {
	return sqsapi.ErrTooManyEntriesInBatchRequest("Maximum number of entries per request are 10. You have sent " + strconv.Itoa(n) + ".")
}

func errInvalidBatchEntryID() *awserr.Error {
	return sqsapi.ErrInvalidBatchEntryId("A batch entry id can only contain alphanumeric characters, hyphens and underscores. It can be at most 80 letters long.")
}

// errBatchIDsNotDistinct is moto's text (sqs.md 6.2).
func errBatchIDsNotDistinct(id string) *awserr.Error {
	return sqsapi.ErrBatchEntryIdsNotDistinct("Id " + id + " repeated.")
}

func errBatchTooLong(n int) *awserr.Error {
	return sqsapi.ErrBatchRequestTooLong("Batch requests cannot be longer than 1048576 bytes. You have sent " + strconv.Itoa(n) + " bytes.")
}

func errPurgeInProgress(name string) *awserr.Error {
	return sqsapi.ErrPurgeQueueInProgress("Only one PurgeQueue operation on " + name + " is allowed every 60 seconds.")
}

func errLabelExists(label string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + label + " for parameter Label is invalid. Reason: Already exists.")
}

func errLabelNotFound(label string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + label + " for parameter Label is invalid. Reason: can't find label.")
}

// errInvalidAction is LocalStack's text and errTooManyActions moto's
// (sqs.md 5.3, 11).
func errInvalidAction(action string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value SQS:" + action + " for parameter ActionName is invalid. Reason: Please refer to the appropriate WSDL for a list of valid actions.")
}

// errPermissionLimit refuses an AddPermission whose statement would take the
// queue's Policy past its documented size or statement limit. The model
// documents OverLimit as what AddPermission returns "if the maximum number
// of permissions for the queue is reached"; the text is inferred.
func errPermissionLimit(reason string) *awserr.Error {
	return sqsapi.ErrOverLimit("The permission cannot be added: " + reason)
}

func errTooManyActions(n int) *awserr.Error {
	return sqsapi.ErrOverLimit(strconv.Itoa(n) + " Actions were found, maximum allowed is 7.")
}

// errTooManyTags is moto's text (sqs.md 5.3).
func errTooManyTags(name string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Too many tags added for queue " + name + ".")
}

// errTagKeyLength carries moto's empty-key reason; errTagValueLength,
// errTagCharacters and errTagKeyReserved state the documented tag rules
// (sqs.md 5.3). All four are framed as the recorded "Value X for parameter P
// is invalid" texts are (inferred).
func errTagKeyLength(key string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + key + " for parameter TagKey is invalid. Reason: Tag keys must be between 1 and 128 characters in length.")
}

func errTagValueLength(value string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + value + " for parameter TagValue is invalid. Reason: Tag values must be between 0 and 256 characters in length.")
}

func errTagCharacters(param, text string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + text + " for parameter " + param + " is invalid. Reason: Tag keys and values can only include letters, digits, whitespace and _ . : / = + - @.")
}

func errTagKeyReserved(key string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + key + " for parameter TagKey is invalid. Reason: The aws: prefix is reserved for AWS use.")
}

// errNextToken refuses a token no listing issued (inferred).
func errNextToken() *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Invalid NextToken value.")
}

func errNotDeadLetterQueue() *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Source queue must be configured as a Dead Letter Queue.")
}

func errTaskRunning() *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("There is already a task running. Only one active task is allowed for a source queue arn at a given time.")
}

func errNoSuchArn(param string) *awserr.Error {
	return sqsapi.ErrResourceNotFoundException("The resource that you specified for the " + param + " parameter doesn't exist.")
}

// errMoveQueueTypes refuses a destination of another queue type than the
// source, which the documentation requires (inferred text).
func errMoveQueueTypes() *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("The source queue and the destination queue must be of the same queue type.")
}

// errMoveToSource refuses a DestinationArn that is the SourceArn
// (inferred text).
func errMoveToSource() *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("The destination queue must be different from the source queue.")
}

func errTaskHandle() *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value for parameter TaskHandle is invalid.")
}

func errNoSuchTask() *awserr.Error {
	return sqsapi.ErrResourceNotFoundException("Task does not exist.")
}
