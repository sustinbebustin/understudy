package ses

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// record is a Message as the "ses" snapshot section holds it (contract
// 1.3): its scope is the cell's. Raw MIME lives in internal/blob, the
// record holding one reference on it; Messages reads it back.
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
	// Attachments are v2 Simple attachments, each content in its own blob.
	Attachments []attachmentRecord `json:"attachments,omitempty"`
	// Raw is the raw MIME's blob, nil for Simple content.
	Raw *blob.Ref `json:"raw,omitempty"`
	// Template is the template a templated send used, nil otherwise.
	Template *TemplateSend `json:"template,omitempty"`

	Tags      []Tag               `json:"tags,omitempty"`
	Simulator map[string]Scenario `json:"simulator,omitempty"`
}

// attachmentRecord is an Attachment as a record holds it: its content in
// internal/blob, one reference held (contract 1.3).
type attachmentRecord struct {
	FileName                string   `json:"fileName"`
	ContentType             string   `json:"contentType,omitempty"`
	ContentDisposition      string   `json:"contentDisposition,omitempty"`
	ContentDescription      string   `json:"contentDescription,omitempty"`
	ContentID               string   `json:"contentId,omitempty"`
	ContentTransferEncoding string   `json:"contentTransferEncoding,omitempty"`
	Content                 blob.Ref `json:"content"`
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
	// Attachments stay nil when there are none; resolve reads their
	// contents.
	var attachments []Attachment
	if len(r.Attachments) > 0 {
		attachments = make([]Attachment, 0, len(r.Attachments))
	}
	for _, a := range r.Attachments {
		attachments = append(attachments, Attachment{
			FileName:                a.FileName,
			ContentType:             a.ContentType,
			ContentDisposition:      a.ContentDisposition,
			ContentDescription:      a.ContentDescription,
			ContentID:               a.ContentID,
			ContentTransferEncoding: a.ContentTransferEncoding,
		})
	}
	return Message{
		MessageID:   r.MessageID,
		API:         r.API,
		Operation:   r.Operation,
		RequestID:   r.RequestID,
		Scope:       scope,
		Accepted:    r.Accepted,
		Kind:        r.Kind,
		Source:      r.Source,
		From:        slices.Clone(r.From),
		ReplyTo:     slices.Clone(r.ReplyTo),
		ReturnPath:  r.ReturnPath,
		To:          slices.Clone(r.To),
		Cc:          slices.Clone(r.Cc),
		Bcc:         slices.Clone(r.Bcc),
		Envelope:    slices.Clone(r.Envelope),
		Subject:     clone(r.Subject),
		Text:        clone(r.Text),
		HTML:        clone(r.HTML),
		Headers:     slices.Clone(r.Headers),
		Attachments: attachments,
		Template:    r.Template.clone(),
		Tags:        slices.Clone(r.Tags),
		Simulator:   maps.Clone(r.Simulator),
	}
}

// blobRefs is the blob references r holds.
func (r *record) blobRefs() []blob.Ref {
	var refs []blob.Ref
	if r.Raw != nil {
		refs = append(refs, *r.Raw)
	}
	for _, a := range r.Attachments {
		refs = append(refs, a.Content)
	}
	return refs
}

// record appends r, evicting the scope's oldest record and counting it
// when the scope already holds MaxStoredMessages (ses.md 11.2). It returns
// the blob references the evicted record held, which the caller releases
// once the write commits.
func (s *state) record(r record) (released []blob.Ref) {
	if len(s.Messages) >= MaxStoredMessages {
		released = s.Messages[0].blobRefs()
		s.Messages[0] = record{}
		s.Messages = s.Messages[1:]
		s.Evicted++
	}
	s.Messages = append(s.Messages, r)
	return released
}

// putBlob stores data in the blob store, holding one reference for the
// record that will name it.
func (s *Service) putBlob(ctx context.Context, data []byte) (blob.Ref, *awserr.Error) {
	if s.blobs == nil {
		return blob.Ref{}, awserr.Internalf(errors.New("the SES service was built without a blob store, so it cannot keep raw messages; wire kernel.ServiceDeps.Blob"))
	}
	ref, _, err := s.blobs.Put(ctx, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return blob.Ref{}, awserr.Internalf(fmt.Errorf("storing a raw message of %d bytes in the blob store: %w; nothing was recorded", len(data), err))
	}
	return ref, nil
}

// release drops blob references no record holds any more. A failure is
// logged: the blob then outlives its record until the next prune.
func (s *Service) release(ctx context.Context, refs ...blob.Ref) {
	for _, r := range refs {
		if err := s.blobs.Release(r); err != nil {
			s.log.LogAttrs(ctx, slog.LevelWarn, "releasing an SES raw message failed; the blob stays until the next prune",
				slog.String("blob", r.String()), slog.String("error", err.Error()))
		}
	}
}

// readBlob is the bytes of ref.
func (s *Service) readBlob(ref blob.Ref) ([]byte, error) {
	r, err := s.blobs.Open(ref)
	if err != nil {
		return nil, fmt.Errorf("opening raw message blob %s: %w", ref, err)
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading raw message blob %s: %w", ref, err)
	}
	return b, nil
}

// BlobRefs lists one entry per blob reference the scope's records hold,
// which is how a snapshot archives the raw messages beside the section.
func (s *state) BlobRefs() []string {
	var refs []string
	for i := range s.Messages {
		for _, ref := range s.Messages[i].blobRefs() {
			refs = append(refs, ref.String())
		}
	}
	return refs
}

// Partitioned finds BlobRefs by type assertion; this keeps a rename from
// silently dropping the blobs from snapshots.
var _ store.BlobHolder = (*state)(nil)

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
