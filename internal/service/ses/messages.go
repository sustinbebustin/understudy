package ses

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
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
	Data    string
	Charset string
}

// Header is a v2 message header.
type Header struct {
	Name, Value string
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
	Name, Value string
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

// NotImplementedError is a store operation understudy does not implement
// yet.
type NotImplementedError struct{ Op string }

func (e *NotImplementedError) Error() string {
	return "ses: " + e.Op + " is not implemented yet; no message was read or deleted"
}

// Messages returns the page of recorded messages q selects, oldest first;
// not implemented yet.
func (*Service) Messages(MessageQuery) (MessagePage, error) {
	return MessagePage{}, &NotImplementedError{Op: "reading sent messages"}
}

// ClearMessages deletes the messages q selects, ignoring Limit and Cursor,
// and returns how many it deleted. The recipient counter is not touched;
// not implemented yet.
func (*Service) ClearMessages(MessageQuery) (int, error) {
	return 0, &NotImplementedError{Op: "deleting sent messages"}
}
