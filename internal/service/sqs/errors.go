package sqs

import (
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The message builders over the generated constructors. Texts are the
// recorded ones of sqs.md 2.4 unless a comment says otherwise.

func errQueueDoesNotExist() *awserr.Error {
	return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
}

func errQueueExists(attr sqsapi.QueueAttributeName) *awserr.Error {
	return sqsapi.ErrQueueNameExists("A queue already exists with the same name and a different value for attribute " + string(attr))
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

// errRedriveParameterMissing and errMaxReceiveCount carry LocalStack's
// texts: AWS's code, InvalidParameterValue, is recorded, its texts are not
// (sqs.md 5.1 and 9.1).
func errRedriveParameterMissing(key string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("The required parameter '" + key + "' is missing")
}

func errMaxReceiveCount(policy, count string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + policy + " for parameter RedrivePolicy is invalid. Reason: Invalid value for maxReceiveCount: " + count + ", valid values are from 1 to 1000 both inclusive.")
}

func errTwoEncryptionTypes() *awserr.Error {
	return sqsapi.ErrInvalidAttributeName("You can use one type of server-side encryption (SSE) at one time. You can either enable KMS SSE or SQS SSE.")
}

// errFIFOUnsupported is understudy's own answer: FIFO semantics (groups,
// deduplication, sequence numbers) are not implemented, and a FIFO queue
// that behaved like a standard one would fail silently.
func errFIFOUnsupported() *awserr.Error {
	return sqsapi.ErrUnsupportedOperation("understudy does not implement FIFO queues yet; create a standard queue (a name without .fifo and no FifoQueue attribute). Run 'understudy coverage --service sqs' for what is supported.")
}

func errMessageTooLong(maxBytes int32) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("One or more parameters are invalid. Reason: Message must be shorter than " + strconv.Itoa(int(maxBytes)) + " bytes.")
}

// errInvalidMessageContents carries LocalStack's text; AWS's is unrecorded
// (sqs.md 6.1).
func errInvalidMessageContents() *awserr.Error {
	return sqsapi.ErrInvalidMessageContents("Invalid characters found. Valid unicode characters are #x9 | #xA | #xD | #x20 to #xD7FF | #xE000 to #xFFFD | #x10000 to #x10FFFF")
}

func errEmptyAttributeValue(name, dataType string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Message (user) attribute '" + name + "' must contain a non-empty value of type '" + dataType + "'.")
}

// errAttributeDataType and errTooManyAttributes follow moto's texts; AWS's
// are unrecorded (sqs.md 6.1).
func errAttributeDataType(name string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("The message attribute '" + name + "' has an invalid message attribute type, the set of supported type prefixes is Binary, Number, and String.")
}

func errTooManyAttributes(n int) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Number of message attributes [" + strconv.Itoa(n) + "] exceeds the allowed maximum [10].")
}

// errVisibilityTimeout frames the documented range in the recorded
// "Value X for parameter P is invalid" form; the exact text is unrecorded.
func errVisibilityTimeout(v int32) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + strconv.Itoa(int(v)) + " for parameter VisibilityTimeout is invalid. Reason: Must be >= 0 and <= 43200, if provided.")
}

func errInvalidGroupID(id string) *awserr.Error {
	return sqsapi.ErrInvalidParameterValue("Value " + id + " for parameter MessageGroupId is invalid. Reason: MessageGroupId can only include alphanumeric and punctuation characters. 1 to 128 in length.")
}

func errReceiptHandleIsInvalid(handle string) *awserr.Error {
	return sqsapi.ErrReceiptHandleIsInvalid(`The input receipt handle "` + handle + `" is not a valid receipt handle.`)
}
