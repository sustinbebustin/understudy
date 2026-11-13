package dynamodb

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"hash/crc32"
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/shardlog"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Stream is a table stream understudy minted (dynamodb-streams.md 11.1
// item 6): a CreateTable or UpdateTable that enables a stream mints one,
// labeled with the call's admission instant on the emulator clock, in
// place of DynamoDB Local's, whose labels follow the wall clock (8.3). A
// stream stays in its scope's list after its table disables it, and until
// streamRetention after its table is deleted (State.expire).
type Stream struct {
	// Table is the name of the table the stream belongs to.
	Table string
	// Label is the stream's label instant, to the millisecond.
	Label    clock.Instant
	ViewType dynamodbapi.StreamViewType
	// Enabled is false once the table disabled the stream or was deleted.
	Enabled bool
	// TableDeleted is when the table was deleted, nil while it exists: a
	// table created again under the name starts without a stream.
	TableDeleted *clock.Instant
	// log holds the stream's records. Every copy of the Stream shares it,
	// and it is read and changed only under the cell's lock.
	log *streamLog
}

// streamLog is a stream's records and the feed's place in Local's stream.
type streamLog struct {
	// keys is the table's key schema, from Local's answer that enabled the
	// stream; DescribeStream reports it.
	keys []dynamodbstreamsapi.KeySchemaElement
	// seq numbers the shard's records: the dynamodb.streams.sequence-number
	// layout the stream was minted under, which a snapshot keeps with it.
	seq shardlog.DecimalCounter
	// shard is the stream's one shard (dynamodb-streams.md 11.1 item 1).
	shard *shardlog.Shard[streamRecord]
	// feed is how far the feed has read Local's stream of the table.
	feed localFeed
}

// savedStream is a Stream as a snapshot holds it (ADR 0009 amendment of
// 2026-10-27): what
// identifies and describes it, its shard with every untrimmed record, and
// the feed's saved place in Local's stream. A failed drain pending at the
// cut is not kept: a load re-creates the table in Local, and the records
// the drain left there are gone with the old table.
type savedStream struct {
	Table        string                                `json:"table"`
	Label        clock.Instant                         `json:"label"`
	ViewType     dynamodbapi.StreamViewType            `json:"viewType"`
	Enabled      bool                                  `json:"enabled"`
	TableDeleted *clock.Instant                        `json:"tableDeleted,omitempty"`
	Keys         []dynamodbstreamsapi.KeySchemaElement `json:"keys"`
	FirstAtStart bool                                  `json:"firstAtStart,omitempty"`
	Shard        shardlog.ShardState[streamRecord]     `json:"shard"`
	LocalLabel   string                                `json:"localLabel"`
	LocalShard   string                                `json:"localShard,omitempty"`
	LocalLast    string                                `json:"localLast,omitempty"`
}

// streamList is a scope's streams, oldest first, which a snapshot holds as
// a list of savedStream.
type streamList []Stream

// MarshalJSONTo writes l as a list of savedStream.
func (l *streamList) MarshalJSONTo(enc *jsontext.Encoder) error {
	saved := make([]savedStream, 0, len(*l))
	for _, s := range *l {
		saved = append(saved, savedStream{
			Table: s.Table, Label: s.Label, ViewType: s.ViewType, Enabled: s.Enabled, TableDeleted: s.TableDeleted,
			Keys: s.log.keys, FirstAtStart: s.log.seq.FirstAtStart, Shard: s.log.shard.State(),
			LocalLabel: s.log.feed.label, LocalShard: s.log.feed.shard, LocalLast: s.log.feed.last,
		})
	}
	return json.MarshalEncode(enc, saved)
}

// UnmarshalJSONFrom reads what MarshalJSONTo writes, each Stream with a
// log of its own.
func (l *streamList) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var saved []savedStream
	if err := json.UnmarshalDecode(dec, &saved); err != nil {
		return err
	}
	out := make(streamList, 0, len(saved))
	for i := range saved {
		v := &saved[i]
		seq := shardlog.DecimalCounter{Digits: streamSeqDigits, FirstAtStart: v.FirstAtStart}
		out = append(out, Stream{
			Table: v.Table, Label: v.Label, ViewType: v.ViewType, Enabled: v.Enabled, TableDeleted: v.TableDeleted,
			log: &streamLog{
				keys: v.Keys, seq: seq, shard: shardlog.RestoreShard(v.Shard, seq),
				feed: localFeed{label: v.LocalLabel, shard: v.LocalShard, last: v.LocalLast},
			},
		})
	}
	*l = out
	return nil
}

// streamRecord is one record of the log: what the feed copied from Local's
// record and what understudy stamps itself (dynamodb-streams.md 11.1 item
// 4). The sequence number and arrival instant are the shard entry's.
type streamRecord struct {
	EventID   string `json:"eventID"`
	EventName string `json:"eventName"`
	Keys      image  `json:"keys"`
	NewImage  image  `json:"newImage,omitempty"`
	OldImage  image  `json:"oldImage,omitempty"`
	SizeBytes int64  `json:"sizeBytes"`
	// TTL marks a delete by the TTL sweeper, whose record carries
	// userIdentity (dynamodb-streams.md 7.5).
	TTL bool `json:"ttl,omitempty"`
}

// localFeed is the feed's place in DynamoDB Local's stream of a table.
type localFeed struct {
	// label is Local's LatestStreamLabel, which with Local's fixed account
	// and region names Local's stream (localARN).
	label string
	// shard is Local's shard id; empty until a drain described the stream.
	shard string
	// last is the SequenceNumber of the last Local record the feed
	// appended; empty before the first.
	last string
	// failed is the stamp of the first write whose drain failed while its
	// records are still in Local; nil when the feed is caught up.
	failed *writeStamp
}

// localARN is the ARN of Local's stream of the table, as Local minted it:
// Local refuses understudy's (dynamodb-streams.md 8.3 row 1).
func (s Stream) localARN() string { return localStreamARN(s.Table, s.log.feed.label) }

// localStreamARN is the ARN of Local's stream of table labeled label.
func localStreamARN(table, label string) string {
	return aws.ARN{
		Partition: "aws", Service: "dynamodb", Region: localRegion, Account: localAccount,
		Resource: "table/" + table + "/stream/" + label,
	}.String()
}

// shardID is the stream's one shard id in AWS's form
// shardId-<20 digits>-<8 hex> (dynamodb-streams.md 4.2): the label
// instant's Unix milliseconds and the CRC-32 of the stream ARN, so it is
// the same on every run.
func shardID(arn string, label clock.Instant) string {
	return fmt.Sprintf("shardId-%020d-%08x", label.Wire().UnixMilli(), crc32.ChecksumIEEE([]byte(arn)))
}

// streamLabelLayout is a stream label: the instant in UTC to the
// millisecond, with no zone (dynamodb-streams.md 4.1, aws-doc examples).
const streamLabelLayout = "2006-01-02T15:04:05.000"

// label is s's StreamLabel.
func (s Stream) label() string { return s.Label.Wire().UTC().Format(streamLabelLayout) }

// arn is s's StreamArn in scope, the documented form
// arn:<partition>:dynamodb:<region>:<account>:table/<table>/stream/<label>
// (dynamodb-streams.md 4.1).
func (s Stream) arn(scope aws.Scope) string { return s.parsedARN(scope).String() }

// parsedARN is s's StreamArn in scope as an aws.ARN.
func (s Stream) parsedARN(scope aws.Scope) aws.ARN {
	return aws.ARN{
		Partition: scope.Partition.String(), Service: "dynamodb",
		Region: scope.Region.String(), Account: scope.Account.String(),
		Resource: "table/" + s.Table + "/stream/" + s.label(),
	}
}

// latestStream is the newest stream of the table name that exists now,
// false when it has none: a table never streamed, or one created again
// after its namesake was deleted. A stream it disabled stays its latest
// (dynamodb-streams.md 5.3, 9.3).
func (st *State) latestStream(name string) (Stream, bool) {
	for _, v := range slices.Backward(st.Streams) {
		if s := v; s.Table == name && s.TableDeleted == nil {
			return s, true
		}
	}
	return Stream{}, false
}

// streamLabel is the label instant of a stream of the table name enabled
// at now: now to the millisecond, plus 1 ms until no stream of a table of
// that name has it, so no two stream ARNs are equal (dynamodb-streams.md
// 11.1 item 6).
func (st *State) streamLabel(name string, now clock.Instant) clock.Instant {
	label := clock.FromWire(now.Wire().UTC().Truncate(time.Millisecond))
	taken := func(s Stream) bool { return s.Table == name && s.Label.Compare(label) == 0 }
	for slices.ContainsFunc(st.Streams, taken) {
		label = label.Add(time.Millisecond)
	}
	return label
}

// disableStream disables the enabled stream of the table name in scope, if
// it has one, closing its shard at (dynamodb-streams.md 5.3). It returns
// the shardlog.WaitKey of each stream it closed, for the caller to wake,
// so an event source mapping's reader finds the stream closed
// (lambda-stream-sources.md 7.3).
func (st *State) disableStream(scope aws.Scope, name string, at clock.Instant) []store.WaitKey {
	var closed []store.WaitKey
	for i := range st.Streams {
		if s := &st.Streams[i]; s.Table == name && s.TableDeleted == nil {
			s.Enabled = false
			s.log.shard.Close(at)
			closed = append(closed, shardlog.WaitKey(s.parsedARN(scope)))
		}
	}
	return closed
}

// wakeAll wakes each of keys once tx commits.
func wakeAll(tx *kernel.Tx, keys []store.WaitKey) {
	for _, key := range keys {
		tx.Wake(key)
	}
}

// tableDeleted marks every stream of the table name in scope, deleted at
// at, disabled and gone with its table, closing its shard
// (dynamodb-streams.md 5.4). It returns the shardlog.WaitKey of each
// stream it closed, as disableStream does.
func (st *State) tableDeleted(scope aws.Scope, name string, at clock.Instant) []store.WaitKey {
	var closed []store.WaitKey
	for i := range st.Streams {
		if s := &st.Streams[i]; s.Table == name && s.TableDeleted == nil {
			s.Enabled, s.TableDeleted = false, &at
			s.log.shard.Close(at)
			closed = append(closed, shardlog.WaitKey(s.parsedARN(scope)))
		}
	}
	return closed
}

// streamRetention is how long a stream keeps a record, and how long a
// deleted table's stream outlives the table: 24 hours ("Stream records
// ... are removed automatically after 24 hours"; "the stream is
// automatically deleted after 24 hours", aws-doc, dynamodb-streams.md 5.4,
// 5.5).
const streamRetention = 24 * time.Hour

// expire applies the stream lifetimes as of now and reports whether it
// changed anything: a deleted table's stream is gone exactly
// streamRetention after the delete, a record exactly streamRetention after
// its arrival, and a used shard iterator is forgotten once it has expired.
// Every Streams operation and every drain runs it under the cell's lock
// first, so the lifetimes hold to the instant on the emulator clock with
// no timer.
func (st *State) expire(now clock.Instant) bool {
	horizon := now.Add(-streamRetention)
	n := len(st.Streams)
	st.Streams = slices.DeleteFunc(st.Streams, func(s Stream) bool {
		return s.TableDeleted != nil && s.TableDeleted.Compare(horizon) <= 0
	})
	changed := len(st.Streams) != n
	for _, s := range st.Streams {
		changed = s.log.trim(horizon) || changed
	}
	return st.forgetUsed(now) || changed
}

// trim drops the records that arrived at or before through and reports
// whether there were any.
func (l *streamLog) trim(through clock.Instant) bool {
	// A read from TrimHorizon is never behind the trim point.
	first, _, err := l.shard.Read(shardlog.TrimHorizon{}, 1, 1, recordSize)
	if err != nil || len(first) == 0 || first[0].Arrival.Compare(through) > 0 {
		return false
	}
	l.shard.Trim(through)
	return true
}

// streamChange is what a StreamSpecification asks of a table's stream.
//
//sumtype:decl
type streamChange interface{ isStreamChange() }

// streamKept is a request without a StreamSpecification.
type streamKept struct{}

// streamEnabled enables a stream of the view type.
type streamEnabled struct{ viewType dynamodbapi.StreamViewType }

// streamDisabled disables the table's stream.
type streamDisabled struct{}

func (streamKept) isStreamChange()     {}
func (streamEnabled) isStreamChange()  {}
func (streamDisabled) isStreamChange() {}

// streamChangeOf reads spec. An enabling spec without a view type is
// streamKept: DynamoDB Local refuses it (dynamodb-streams.md 5.1), so no
// stream is minted.
func streamChangeOf(spec *dynamodbapi.StreamSpecification) streamChange {
	switch {
	case spec == nil:
		return streamKept{}
	case !spec.StreamEnabled:
		return streamDisabled{}
	case spec.StreamViewType == nil:
		return streamKept{}
	}
	return streamEnabled{viewType: *spec.StreamViewType}
}

// streamMembers are a table description's LatestStreamArn and
// LatestStreamLabel.
type streamMembers struct{ arn, label string }

// members are s's LatestStreamArn and LatestStreamLabel in scope.
func (s Stream) members(scope aws.Scope) *streamMembers {
	return &streamMembers{arn: s.arn(scope), label: s.label()}
}

// latestStream is the members of the latest stream of the table name in
// scope; nil when it has none, which leaves Local's members as they are.
func (s *Service) latestStream(scope aws.Scope, name string) *streamMembers {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) *streamMembers {
		if l, ok := st.latestStream(name); ok {
			return l.members(scope)
		}
		return nil
	})
}

// mint is the stream of view type v that a call admitted at now enables on
// the table name in scope. The call records it once Local accepts the
// change; Local refuses a second enabling call on the table, so no other
// call mints the same label meanwhile.
func (s *Service) mint(scope aws.Scope, name string, v dynamodbapi.StreamViewType, now clock.Instant) Stream {
	label := clock.FromWire(now.Wire().UTC().Truncate(time.Millisecond))
	if cell, ok := s.cells.Lookup(scope); ok {
		label = store.Read(cell, func(st *State) clock.Instant { return st.streamLabel(name, now) })
	}
	return newStream(scope, name, v, label, s.sequencer())
}

// newStream is an enabled stream of view type v on the table name in scope,
// labeled label, with an empty open shard numbered by seq.
func newStream(scope aws.Scope, name string, v dynamodbapi.StreamViewType, label clock.Instant, seq shardlog.DecimalCounter) Stream {
	st := Stream{Table: name, Label: label, ViewType: v, Enabled: true}
	info := shardlog.ShardInfo{ID: shardID(st.arn(scope), label), Opened: label}
	st.log = &streamLog{seq: seq, shard: shardlog.NewShard[streamRecord](info, seq)}
	return st
}

// sequencer numbers stream records as the dynamodb.streams.sequence-number
// switch says (dynamodb-streams.md 4.3, DDBS-09).
func (s *Service) sequencer() shardlog.DecimalCounter {
	return shardlog.DecimalCounter{Digits: streamSeqDigits, FirstAtStart: s.streamSeq == SequenceStartEqualsFirst}
}

// streamSeqDigits is the length of a shard's StartingSequenceNumber under
// either value of the switch: 21 digits, as AWS's examples start
// (dynamodb-streams.md 4.3).
const streamSeqDigits = 21

// adopt records what Local's answer said of the stream minted by the call
// it answered (seen), so the feed can find Local's stream.
func (s Stream) adopt(seen *localStreamSeen) {
	s.log.keys = seen.keys
	s.log.feed.label = seen.label
}

// relink is a table ImportLate re-created in Local with a stream: Local's
// new stream, read to its tip, and what a stream minted for it needs.
type relink struct {
	feed     localFeed
	keys     []dynamodbstreamsapi.KeySchemaElement
	viewType dynamodbapi.StreamViewType
}

// relink maps the enabled stream of table to r's Local stream, at its tip
// so the load's own writes emit nothing, keeping its label
// (dynamodb-streams.md 11.1 item 7). A table without one, as every table
// of a version 3 section, gets a fresh stream labeled at now, the import
// instant (ADR 0009 amendment of 2026-10-27).
func (st *State) relink(scope aws.Scope, table string, r relink, now clock.Instant, seq shardlog.DecimalCounter) {
	if l, ok := st.latestStream(table); ok && l.Enabled {
		l.log.feed = r.feed
		return
	}
	minted := newStream(scope, table, r.viewType, st.streamLabel(table, now), seq)
	minted.log.keys, minted.log.feed = r.keys, r.feed
	st.Streams = append(st.Streams, minted)
}
