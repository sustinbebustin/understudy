package ses

import (
	"cmp"
	"fmt"
	"net/mail"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/store"
)

// API is which SES API a message was accepted by.
type API string

// API values, the service ids.
const (
	APIv1 API = "ses"
	APIv2 API = "sesv2"
)

// Kind is what put a message in the store.
type Kind string

// Kind values.
const (
	// KindSend is a message an accepted send recorded.
	KindSend Kind = "send"
	// KindVerification is the verification email of an email identity,
	// recorded when VerifyEmailIdentity or CreateEmailIdentity starts or
	// restarts its verification; it counts toward no quota.
	KindVerification Kind = "verification"
)

// Message is one recorded message with its bodies resolved (ses.md 11.2).
// Members a message kind or API does not have are zero.
type Message struct {
	MessageID string
	API       API
	// Operation is the operation that recorded it, such as "SendRawEmail"
	// or "VerifyEmailIdentity".
	Operation string
	RequestID ids.RequestID
	Scope     aws.Scope
	// Accepted is the emulator time of acceptance.
	Accepted clock.Instant
	Kind     Kind

	// Source is v1 Source, v2 FromEmailAddress, or empty for a raw send
	// without one, as given.
	Source string
	// From is the addr-specs of the sender: Source parsed, else the raw
	// From header's addresses.
	From []string
	// ReplyTo, To, Cc and Bcc are as given in the request, or the raw
	// headers' addresses as written.
	ReplyTo []string
	// ReturnPath is v1 ReturnPath, v2 FeedbackForwardingEmailAddress, or
	// the raw Return-Path header, as given.
	ReturnPath  string
	To, Cc, Bcc []string
	// Envelope is the recipients the sandbox check and the counter used,
	// as given.
	Envelope []string

	// Subject, Text and HTML are Simple content; nil when absent.
	Subject, Text, HTML *Content
	// Headers are v2 Simple Headers in request order.
	Headers []Header
	// Attachments are v2 Simple Attachments in request order.
	Attachments []Attachment
	// Raw is the raw MIME as received, without SES's substituted
	// Message-ID and Date headers; nil for Simple content.
	Raw []byte

	// Tags are the send's tags in request order, then any
	// X-SES-MESSAGE-TAGS of a raw send.
	Tags []Tag
	// Simulator maps each simulator recipient, as given, to its scenario.
	Simulator map[string]Scenario
}

// Content is a subject or body with its charset ("" when not given).
type Content struct {
	Data    string `json:"data"`
	Charset string `json:"charset,omitempty"`
}

// Header is a v2 message header.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Attachment is a v2 attachment with its content.
type Attachment struct {
	FileName                string
	ContentType             string
	ContentDisposition      string
	ContentDescription      string
	ContentID               string
	ContentTransferEncoding string
	Content                 []byte
}

// Tag is a message tag.
type Tag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// MessageQuery selects recorded messages. Zero members select everything.
type MessageQuery struct {
	// Account and Region narrow to one account or Region.
	Account aws.AccountID
	Region  aws.Region
	// MessageID selects one message.
	MessageID string
	// From matches a message whose From or Source contains this address,
	// compared as the identity rules compare (ses.md 5.1).
	From string
	// To matches a message with this address among To, Cc, Bcc or the
	// envelope, compared as From is.
	To string
	// Since selects messages accepted at or after this emulator time.
	Since clock.Instant
	// Limit is the page size; 0 is DefaultMessageLimit. Clear ignores it.
	Limit int
	// Cursor is MessagePage.Next of the previous page. Clear ignores it.
	Cursor string
}

// DefaultMessageLimit and MaxMessageLimit are a message page's default and
// largest size.
const (
	DefaultMessageLimit = 100
	MaxMessageLimit     = 1000
)

// MessagePage is a page of messages, oldest first.
type MessagePage struct {
	Messages []Message
	// Next is the Cursor of the next page, "" when there is none.
	Next string
	// Evicted is how many messages the store has evicted in the account
	// and Region scopes the query covers.
	Evicted int
}

// QueryError is a MessageQuery the store cannot run: a limit out of range or
// a cursor it did not issue.
type QueryError struct {
	// Field is the admin query parameter: "limit" or "cursor".
	Field string
	// Reason says what is wrong and what to send instead.
	Reason string
}

func (e *QueryError) Error() string { return "ses: message query " + e.Field + ": " + e.Reason }

// Messages returns the page of recorded messages q selects, oldest first:
// in the order the store recorded them, across every account and Region q
// covers. A limit outside 1..MaxMessageLimit or a cursor the store did not
// issue is a *QueryError.
func (s *Service) Messages(q MessageQuery) (MessagePage, error) {
	limit := q.Limit
	switch {
	case limit == 0:
		limit = DefaultMessageLimit
	case limit < 1 || limit > MaxMessageLimit:
		return MessagePage{}, &QueryError{
			Field:  "limit",
			Reason: fmt.Sprintf("%d is not a page size; send 1 to %d, or leave it out for pages of %d", q.Limit, MaxMessageLimit, DefaultMessageLimit),
		}
	}
	after, err := parseCursor(q.Cursor)
	if err != nil {
		return MessagePage{}, err
	}
	type hit struct {
		scope aws.Scope
		r     record
	}
	var hits []hit
	evicted := 0
	for _, scope := range s.covered(q) {
		cell, ok := s.cells.Lookup(scope)
		if !ok {
			continue
		}
		store.Read(cell, func(st *state) struct{} {
			evicted += st.Evicted
			for i := range st.Messages {
				if r := &st.Messages[i]; r.Seq > after && q.selects(r) {
					hits = append(hits, hit{scope: scope, r: *r})
				}
			}
			return struct{}{}
		})
	}
	slices.SortFunc(hits, func(a, b hit) int { return cmp.Compare(a.r.Seq, b.r.Seq) })
	page := MessagePage{Messages: []Message{}, Evicted: evicted}
	if len(hits) > limit {
		hits = hits[:limit]
		page.Next = cursorAfter(hits[limit-1].r.Seq)
	}
	for i := range hits {
		page.Messages = append(page.Messages, hits[i].r.message(hits[i].scope))
	}
	return page, nil
}

// ClearMessages deletes the messages q selects, ignoring Limit and Cursor,
// and returns how many it deleted. The recipient counter and the evicted
// counts are not touched. The error is always nil: every query a
// MessageQuery can spell without Limit and Cursor is valid.
func (s *Service) ClearMessages(q MessageQuery) (int, error) {
	deleted := 0
	for _, scope := range s.covered(q) {
		cell, ok := s.cells.Lookup(scope)
		if !ok {
			continue
		}
		deleted += store.Update(cell, func(st *state) (int, bool) {
			kept := st.Messages[:0]
			for i := range st.Messages {
				if !q.selects(&st.Messages[i]) {
					kept = append(kept, st.Messages[i])
				}
			}
			n := len(st.Messages) - len(kept)
			clear(st.Messages[len(kept):])
			st.Messages = kept
			return n, n > 0
		})
	}
	return deleted, nil
}

// covered is the scopes holding state that q's Account and Region select.
func (s *Service) covered(q MessageQuery) []aws.Scope {
	var out []aws.Scope
	for _, scope := range s.Scopes() {
		if (q.Account.IsZero() || scope.Account == q.Account) && (q.Region.IsZero() || scope.Region == q.Region) {
			out = append(out, scope)
		}
	}
	return out
}

// selects reports whether r passes q's MessageID, From, To and Since.
func (q *MessageQuery) selects(r *record) bool {
	switch {
	case q.MessageID != "" && r.MessageID != q.MessageID:
		return false
	case q.From != "" && !anyAddress(q.From, r.From, []string{r.Source}):
		return false
	case q.To != "" && !anyAddress(q.To, r.To, r.Cc, r.Bcc, r.Envelope):
		return false
	case !q.Since.IsZero() && r.Accepted.Before(q.Since):
		return false
	}
	return true
}

// anyAddress reports whether one of lists holds an address that names the
// same mailbox as want.
func anyAddress(want string, lists ...[]string) bool {
	w := addrSpec(want)
	for _, l := range lists {
		for _, a := range l {
			if a != "" && sameMailbox(w, addrSpec(a)) {
				return true
			}
		}
	}
	return false
}

// addrSpec is the addr-spec of a, which may carry a display name; a as
// given when it does not parse.
func addrSpec(a string) string {
	if p, err := mail.ParseAddress(a); err == nil {
		return p.Address
	}
	return a
}

// sameMailbox compares two addr-specs as SES compares identities (ses.md
// 5.1): the local part byte for byte, the domain without regard to case.
// No +label is removed: a filter names the address it was sent to.
func sameMailbox(a, b string) bool {
	ai, bi := strings.LastIndexByte(a, '@'), strings.LastIndexByte(b, '@')
	if ai < 0 || bi < 0 {
		return a == b
	}
	return a[:ai] == b[:bi] && strings.EqualFold(a[ai+1:], b[bi+1:])
}

// cursorPrefix starts every cursor the store issues.
const cursorPrefix = "m"

// cursorAfter is the cursor of the page after the message numbered seq.
func cursorAfter(seq uint64) string { return cursorPrefix + strconv.FormatUint(seq, 10) }

// parseCursor reads a cursor of cursorAfter; "" is the first page.
func parseCursor(c string) (uint64, error) {
	if c == "" {
		return 0, nil
	}
	digits, ok := strings.CutPrefix(c, cursorPrefix)
	n, err := strconv.ParseUint(digits, 10, 64)
	if !ok || err != nil || n == 0 {
		return 0, &QueryError{Field: "cursor", Reason: fmt.Sprintf("%q is not a cursor this store issued; send the next value of the previous page, or leave it out for the first page", c)}
	}
	return n, nil
}
