package kinesis

import (
	"maps"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// streamRef is the stream a stream-scoped input names (contract 3.3).
type streamRef struct {
	name string
	// account names the stream in the not-found text: the ARN's when one
	// was sent, else the caller's.
	account string
	// foreign is an ARN of another partition, account or region than the
	// caller's: such a stream is never found, since the caller's scope is
	// the only one looked in.
	foreign bool
}

func (r streamRef) notFound() *awserr.Error { return errStreamNotFound(r.name, r.account) }

// streamOf reads StreamName and StreamARN: neither is refused, an ARN must
// name a stream, and both must name the same one ("you must use either
// the StreamARN or the StreamName parameter, or both", aws-doc kinesis.md
// 3.1; refusal texts understudy's, KIN-08). StreamId is never read
// (kinesis.md 3.1, "Not Implemented. Reserved").
func streamOf(c *kernel.Call, name, arn *string) (streamRef, *awserr.Error) {
	scope := c.Scope()
	switch {
	case arn != nil:
		a, ok := parseStreamARN(*arn)
		if !ok {
			return streamRef{}, errInvalidStreamARN(*arn)
		}
		sname := strings.TrimPrefix(a.Resource, "stream/")
		if name != nil && *name != sname {
			return streamRef{}, errStreamMismatch(*name, *arn)
		}
		foreign := a.Partition != scope.Partition.String() || a.Account != scope.Account.String() || a.Region != scope.Region.String()
		return streamRef{name: sname, account: a.Account, foreign: foreign}, nil
	case name != nil:
		return streamRef{name: *name, account: scope.Account.String()}, nil
	}
	return streamRef{}, errNoStream()
}

// parseStreamARN accepts arn:<partition>:kinesis:<region>:<account>:stream/<name>
// with a name holding no further slash (kinesis.md 3.1).
func parseStreamARN(s string) (aws.ARN, bool) {
	a, err := aws.ParseARN(s)
	if err != nil || a.Service != "kinesis" {
		return aws.ARN{}, false
	}
	name, ok := strings.CutPrefix(a.Resource, "stream/")
	if !ok || name == "" || strings.Contains(name, "/") {
		return aws.ARN{}, false
	}
	return a, true
}

// readStream returns the stream r names in the caller's scope.
func (s *Service) readStream(c *kernel.Call, r streamRef) (Stream, *awserr.Error) {
	if r.foreign {
		return Stream{}, r.notFound()
	}
	str := readState(s, c.Scope(), func(st *State) *Stream {
		str, ok := st.Streams[r.name]
		if !ok {
			return nil
		}
		return &str
	})
	if str == nil {
		return Stream{}, r.notFound()
	}
	c.Touch(streamARN(c.Scope(), str.Name))
	return *str, nil
}

// updateStream replaces the stream r names with what fn makes of a copy
// whose Tags fn may change in place; fn's error leaves the stream as it
// was.
func (s *Service) updateStream(c *kernel.Call, r streamRef, fn func(str *Stream, now clock.Instant) *awserr.Error) *awserr.Error {
	if r.foreign {
		return r.notFound()
	}
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *State, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		cur, ok := st.Streams[r.name]
		if !ok {
			return struct{}{}, r.notFound()
		}
		next := cur
		next.Tags = maps.Clone(cur.Tags)
		if err := fn(&next, now); err != nil {
			return struct{}{}, err
		}
		st.Streams[r.name] = next
		return struct{}{}, nil
	})
	if err != nil {
		return err
	}
	c.Touch(streamARN(c.Scope(), r.name))
	return nil
}

// readState runs fn on the state of scope without creating its cell; a
// scope with no cell reads as empty.
func readState[R any](s *Service, scope aws.Scope, fn func(st *State) R) R {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		st := newState(scope)
		return fn(&st)
	}
	return store.Read(cell, fn)
}
