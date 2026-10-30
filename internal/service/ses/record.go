package ses

import (
	"encoding/hex"
	"fmt"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// record is a Message as the "ses" snapshot section holds it (contract
// 1.3): its scope is the cell's. Raw MIME and attachment contents are not
// members: they belong in internal/blob, with the record holding the
// references, and no operation that records them is bound yet.
type record struct {
	// Seq numbers the records of the whole store in the order they were
	// recorded: the order Messages answers in, and what its cursor names.
	Seq       uint64        `json:"seq"`
	MessageID string        `json:"messageId"`
	API       API           `json:"api"`
	Operation string        `json:"operation"`
	RequestID ids.RequestID `json:"requestId"`
	Accepted  clock.Instant `json:"accepted"`
	Kind      Kind          `json:"kind"`

	Source     string   `json:"source,omitempty"`
	From       []string `json:"from,omitempty"`
	ReplyTo    []string `json:"replyTo,omitempty"`
	ReturnPath string   `json:"returnPath,omitempty"`
	To         []string `json:"to,omitempty"`
	Cc         []string `json:"cc,omitempty"`
	Bcc        []string `json:"bcc,omitempty"`
	Envelope   []string `json:"envelope,omitempty"`

	Subject *Content `json:"subject,omitempty"`
	Text    *Content `json:"text,omitempty"`
	HTML    *Content `json:"html,omitempty"`
	Headers []Header `json:"headers,omitempty"`

	Tags      []Tag               `json:"tags,omitempty"`
	Simulator map[string]Scenario `json:"simulator,omitempty"`
}

// message is r in scope, sharing no memory with the state.
func (r *record) message(scope aws.Scope) Message {
	clone := func(c *Content) *Content {
		if c == nil {
			return nil
		}
		v := *c
		return &v
	}
	return Message{
		MessageID:  r.MessageID,
		API:        r.API,
		Operation:  r.Operation,
		RequestID:  r.RequestID,
		Scope:      scope,
		Accepted:   r.Accepted,
		Kind:       r.Kind,
		Source:     r.Source,
		From:       slices.Clone(r.From),
		ReplyTo:    slices.Clone(r.ReplyTo),
		ReturnPath: r.ReturnPath,
		To:         slices.Clone(r.To),
		Cc:         slices.Clone(r.Cc),
		Bcc:        slices.Clone(r.Bcc),
		Envelope:   slices.Clone(r.Envelope),
		Subject:    clone(r.Subject),
		Text:       clone(r.Text),
		HTML:       clone(r.HTML),
		Headers:    slices.Clone(r.Headers),
		Tags:       slices.Clone(r.Tags),
		Simulator:  maps.Clone(r.Simulator),
	}
}

// record appends r, evicting the scope's oldest record and counting it
// when the scope already holds MaxStoredMessages (ses.md 11.2).
func (s *state) record(r record) {
	if len(s.Messages) >= MaxStoredMessages {
		s.Messages[0] = record{}
		s.Messages = s.Messages[1:]
		s.Evicted++
	}
	s.Messages = append(s.Messages, r)
}

// newRecord starts the record of a message call c accepts at now: its
// number, MessageId, API, operation and request id.
func (s *Service) newRecord(c *kernel.Call, api API, kind Kind, now clock.Instant) record {
	return record{
		Seq:       s.seq.Add(1),
		MessageID: s.messageID(c.Scope().Region, now),
		API:       api,
		Operation: c.OperationID().Name,
		RequestID: c.ID(),
		Accepted:  now,
		Kind:      kind,
	}
}

// messageID mints a MessageId per switch ses.message-id.format (ses.md
// 4.7): a 16-hex-digit prefix, "-", a version-4 UUID and "-000000", 60
// characters.
func (s *Service) messageID(r aws.Region, now clock.Instant) string {
	prefix := "01" + MessageIDRegionCode(r) + fmt.Sprintf("%012x", now.Wire().UnixMilli())
	if s.opts.MessageID == MessageIDRandom {
		prefix = hex.EncodeToString(s.ids.Draw(ids.KindSESMessageID, 8))
	}
	return prefix + "-" + s.ids.UUID(ids.KindSESMessageID).String() + "-000000"
}

// maxSeq is the largest record number in s, 0 when it holds none.
func (s *state) maxSeq() uint64 {
	var n uint64
	for i := range s.Messages {
		n = max(n, s.Messages[i].Seq)
	}
	return n
}
