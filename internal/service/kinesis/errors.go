package kinesis

import (
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
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

// errShardLimit refuses a CreateStream that would take the scope's shards
// above shardLimit (aws-doc kinesis.md 4.1); the text is understudy's,
// UNVERIFIED (KIN-23).
func errShardLimit(scope aws.Scope, open, adding int) *awserr.Error {
	return kinesisapi.ErrLimitExceededException("This request would exceed the shard limit for the account " + scope.Account.String() + " in " + scope.Region.String() +
		". Current shard count for the account: " + strconv.Itoa(open) + ". Limit: " + strconv.Itoa(shardLimit) +
		". Number of additional shards that would have resulted from this request: " + strconv.Itoa(adding) + ".")
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

// The PutRecord and PutRecords refusals (kinesis.md 13.3): kinesalite's
// ExplicitHashKey text (KIN-07), understudy's partition-key (KIN-07) and
// size texts (KIN-14).
func errExplicitHashKey(v string) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Invalid ExplicitHashKey. ExplicitHashKey must be in the range: [0, 2^128-1]. Specified value was " + v)
}

func errNoPartitionKey() *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("PartitionKey is required for a stream whose RecordDistributionStrategy is USER_PARTITION_KEY.")
}

func errRecordTooLarge(n, maxBytes int) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Record size " + strconv.Itoa(n) + " bytes (data plus partition key) exceeds the stream's maximum record size of " + strconv.Itoa(maxBytes) + " bytes.")
}

func errPutRecordsTooLarge(n int) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("PutRecords request size " + strconv.Itoa(n) + " bytes (data plus partition keys) exceeds the limit of 10485760 bytes.")
}

// The GetShardIterator refusals (kinesis.md 13.3): kinesalite's shard and
// no-number texts (KIN-05, KIN-12), understudy's for the rest (KIN-12).
func errShardNotFound(shard, name, account string) *awserr.Error {
	return kinesisapi.ErrResourceNotFoundException("Could not find shard " + shard + " in stream " + name + " under account " + account + ".")
}

// iteratorCombo is the sentence every iterator type and number mismatch
// starts with.
const iteratorCombo = "Must either specify (1) AT_SEQUENCE_NUMBER or AFTER_SEQUENCE_NUMBER and StartingSequenceNumber or (2) TRIM_HORIZON or LATEST and no StartingSequenceNumber. Request specified "

func errIteratorNoNumber(typ kinesisapi.ShardIteratorType) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException(iteratorCombo + string(typ) + " and no StartingSequenceNumber.")
}

func errIteratorExtraNumber(typ kinesisapi.ShardIteratorType) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException(iteratorCombo + string(typ) + " and also a StartingSequenceNumber.")
}

func errIteratorNoTimestamp() *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Must specify timestampInMillis parameter for iterator of type AT_TIMESTAMP. Current request has no timestamp parameter.")
}

func errIteratorFutureTimestamp(ts, now int64) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("The timestampInMillis parameter cannot be greater than the currentTimestampInMillis. timestampInMillis: " +
		strconv.FormatInt(ts, 10) + ", currentTimestampInMillis: " + strconv.FormatInt(now, 10))
}

// errSequenceInvalid is a StartingSequenceNumber the shard cannot start
// at; why is empty, or " because it has been trimmed".
func errSequenceInvalid(seq, shard, name, account, why string) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("StartingSequenceNumber " + seq + " used in GetShardIterator on shard " + shard + " in stream " + name + " under account " + account + " is invalid" + why + ".")
}

func errSequenceOtherShard(index int, shard string) *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Invalid StartingSequenceNumber. It encodes " + shardID(index) + ", while it was used in a call to a shard with " + shard)
}

// The GetRecords refusals (kinesis.md 5.6, 13.3): kinesalite's texts
// (KIN-11, KIN-12), its expiry instants in java.util.Date's form.
func errInvalidIterator() *awserr.Error {
	return kinesisapi.ErrInvalidArgumentException("Invalid ShardIterator.")
}

// javaDate is java.util.Date.toString's form in UTC.
const javaDate = "Mon Jan 02 15:04:05 UTC 2006"

func errIteratorExpired(issued, now clock.Instant) *awserr.Error {
	return kinesisapi.ErrExpiredIteratorException("Iterator expired. The iterator was created at time " + issued.Wire().UTC().Format(javaDate) +
		" while right now it is " + now.Wire().UTC().Format(javaDate) + " which is further in the future than the tolerated delay of 300000 milliseconds.")
}

func errShardGone(shard, name, account string) *awserr.Error {
	return kinesisapi.ErrResourceNotFoundException("Shard " + shard + " in stream " + name + " under account " + account + " does not exist")
}
