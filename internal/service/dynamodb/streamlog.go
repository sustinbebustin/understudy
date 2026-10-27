package dynamodb

import (
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Stream is a table stream understudy minted (dynamodb-streams.md 11.1
// item 6): a CreateTable or UpdateTable that enables a stream mints one,
// labeled with the call's admission instant on the emulator clock, in
// place of DynamoDB Local's, whose labels follow the wall clock (8.3). A
// stream stays in its scope's list after its table disables it or is
// deleted.
type Stream struct {
	// Table is the name of the table the stream belongs to.
	Table string
	// Label is the stream's label instant, to the millisecond.
	Label    clock.Instant
	ViewType dynamodbapi.StreamViewType
	// Enabled is false once the table disabled the stream or was deleted.
	Enabled bool
	// TableGone is set when the table was deleted: a table created again
	// under the name starts without a stream.
	TableGone bool
}

// streamLabelLayout is a stream label: the instant in UTC to the
// millisecond, with no zone (dynamodb-streams.md 4.1, aws-doc examples).
const streamLabelLayout = "2006-01-02T15:04:05.000"

// label is s's StreamLabel.
func (s Stream) label() string { return s.Label.Wire().UTC().Format(streamLabelLayout) }

// arn is s's StreamArn in scope, the documented form
// arn:<partition>:dynamodb:<region>:<account>:table/<table>/stream/<label>
// (dynamodb-streams.md 4.1).
func (s Stream) arn(scope aws.Scope) string {
	return aws.ARN{
		Partition: scope.Partition.String(), Service: "dynamodb",
		Region: scope.Region.String(), Account: scope.Account.String(),
		Resource: "table/" + s.Table + "/stream/" + s.label(),
	}.String()
}

// latestStream is the newest stream of the table name that exists now,
// false when it has none: a table never streamed, or one created again
// after its namesake was deleted. A stream it disabled stays its latest
// (dynamodb-streams.md 5.3, 9.3).
func (st *State) latestStream(name string) (Stream, bool) {
	for _, v := range slices.Backward(st.Streams) {
		if s := v; s.Table == name && !s.TableGone {
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

// disableStream disables the enabled stream of the table name, if it has
// one.
func (st *State) disableStream(name string) {
	for i := range st.Streams {
		if s := &st.Streams[i]; s.Table == name && !s.TableGone {
			s.Enabled = false
		}
	}
}

// tableDeleted marks every stream of the table name disabled and gone with
// its table.
func (st *State) tableDeleted(name string) {
	for i := range st.Streams {
		if s := &st.Streams[i]; s.Table == name {
			s.Enabled, s.TableGone = false, true
		}
	}
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
	return Stream{Table: name, Label: label, ViewType: v, Enabled: true}
}

// streams is a copy of the streams minted in scope, oldest first.
func (s *Service) streams(scope aws.Scope) []Stream {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) []Stream { return slices.Clone(st.Streams) })
}
