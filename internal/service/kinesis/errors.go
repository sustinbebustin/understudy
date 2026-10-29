package kinesis

import (
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The hand-owned error texts. Each is kinesalite's as kinesis.md quotes it
// or understudy's; both are UNVERIFIED (kinesis.md 13.3), and each names
// its recording-queue item.

// errStreamNotFound names the stream and account, from the ARN when one was
// sent (kinesis.md finding 5: recorded over CBOR, the JSON text inferred
// identical, KIN-05).
func errStreamNotFound(name, account string) *awserr.Error {
	return kinesisapi.ErrResourceNotFoundException("Stream " + name + " under account " + account + " not found.")
}

// errStreamInUse is kinesalite's text for a taken name (kinesis.md 13.3,
// KIN-05).
func errStreamInUse(name, account string) *awserr.Error {
	return kinesisapi.ErrResourceInUseException("Stream " + name + " under account " + account + " already exists.")
}

// errNotImplemented is the 501 of a setting slice 1 does not build
// (contract 3.7).
func errNotImplemented(setting, op string) *awserr.Error {
	return kinesisapi.ErrNotImplemented("understudy does not implement " + setting + " on " + op + ". Run 'understudy coverage --service kinesis' for what is supported.")
}

// The stream identification refusals are understudy's (kinesis.md 3.1,
// KIN-08).
func errNoStream() *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Either StreamName or StreamARN must be provided.")
}

func errInvalidStreamARN(arn string) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Invalid StreamARN " + arn + ".")
}

func errStreamMismatch(name, arn string) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("StreamName " + name + " and StreamARN " + arn + " do not name the same stream.")
}

// The CreateStream mode refusals are understudy's (KIN-07).
func errShardCountOnDemand() *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("ShardCount must not be specified for a stream in ON_DEMAND mode.")
}

func errShardCountProvisioned() *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("ShardCount must be specified for a stream in PROVISIONED mode.")
}

// The tag refusals are understudy's (kinesis.md 4.10, KIN-07).
func errReservedTag(key string) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Tag key " + key + " uses the reserved prefix aws:.")
}

func errTooManyTags(name, account string) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Failed to add tags to stream " + name + " under account " + account + " because a given stream cannot have more than 50 tags.")
}

func errInvalidResourceARN(arn string) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Invalid ResourceARN " + arn + ".")
}

// errInvalidNextToken is understudy's text for a token no listing issued
// (KIN-15).
func errInvalidNextToken() *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Invalid NextToken.")
}

func errListStreamsTokenAndStart() *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("NextToken and ExclusiveStartStreamName cannot be provided together.")
}

// The retention refusals (kinesis.md 4.7, KIN-06): kinesalite's minimum
// and shorter/longer texts, understudy's mirror for the maximum and the
// equal-value texts of kinesis.retention.equal-value reject.
func hours(n int32) string { return strconv.Itoa(int(n)) + " hours" }

func errRetentionTooShort(n int32) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Minimum allowed retention period is 24 hours. Requested retention period (" + hours(n) + ") is too short.")
}

func errRetentionTooLong(n int32) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Maximum allowed retention period is 8760 hours. Requested retention period (" + hours(n) + ") is too long.")
}

func errRetentionShorter(n int32, name string, cur int32) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Requested retention period (" + hours(n) + ") for stream " + name + " can not be shorter than existing retention period (" + hours(cur) + "). Use DecreaseRetentionPeriod API.")
}

func errRetentionLonger(n int32, name string, cur int32) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Requested retention period (" + hours(n) + ") for stream " + name + " can not be longer than existing retention period (" + hours(cur) + "). Use IncreaseRetentionPeriod API.")
}

func errRetentionEqual(n int32, name, comparison string, cur int32) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Requested retention period (" + hours(n) + ") for stream " + name + " must be " + comparison + " than the existing retention period (" + hours(cur) + ").")
}
