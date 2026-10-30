package admin

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// SESMessageQuery selects SES sent messages for GET and DELETE
// /ses/messages. Zero members select everything; DELETE ignores Limit and
// Cursor.
type SESMessageQuery struct {
	// Account and Region narrow to one account or Region.
	Account string
	Region  string
	// MessageID selects one message.
	MessageID string
	// From selects messages from this address and To messages to it, as a
	// To, Cc or Bcc recipient or an envelope recipient.
	From string
	To   string
	// Since selects messages accepted at or after this emulator time.
	Since time.Time
	// Limit is the page size, 1 to 1000; 0 means the server's default of
	// 100.
	Limit int
	// Cursor is SESMessagePage.Next of the previous page.
	Cursor string
}

// Query parameter names of the SES message endpoints.
const (
	ParamSESAccount   = "account"
	ParamSESRegion    = "region"
	ParamSESMessageID = "messageId"
	ParamSESFrom      = "from"
	ParamSESTo        = "to"
	ParamSESSince     = "since"
)

// Values encodes q as the query parameters the SES message endpoints read:
// Since as RFC 3339 with nanoseconds, Limit and Cursor under ParamLimit and
// ParamCursor. Zero members are left out.
func (q SESMessageQuery) Values() url.Values {
	v := url.Values{}
	set := func(k, s string) {
		if s != "" {
			v.Set(k, s)
		}
	}
	set(ParamSESAccount, q.Account)
	set(ParamSESRegion, q.Region)
	set(ParamSESMessageID, q.MessageID)
	set(ParamSESFrom, q.From)
	set(ParamSESTo, q.To)
	if !q.Since.IsZero() {
		v.Set(ParamSESSince, q.Since.UTC().Format(time.RFC3339Nano))
	}
	if q.Limit != 0 {
		v.Set(ParamLimit, strconv.Itoa(q.Limit))
	}
	set(ParamCursor, q.Cursor)
	return v
}

// SESMessagePage is GET /ses/messages: messages oldest first.
type SESMessagePage struct {
	Messages []SESMessage `json:"messages"`
	// Next is the Cursor of the next page, or empty when there is none.
	Next string `json:"next,omitempty"`
	// Evicted counts the messages the store evicted, at 10,000 per account
	// and Region, in the scopes the query covers.
	Evicted int `json:"evicted"`
}

// SESClearResult is DELETE /ses/messages.
type SESClearResult struct {
	Deleted int `json:"deleted"`
}

// SESMessage is one message SES recorded instead of sending it.
type SESMessage struct {
	MessageID string `json:"messageId"`
	// API is "ses" (v1) or "sesv2".
	API string `json:"api"`
	// Operation is the operation that recorded the message, such as
	// "SendEmail" or "VerifyEmailIdentity".
	Operation string    `json:"operation"`
	RequestID string    `json:"requestId"`
	Account   string    `json:"account"`
	Region    string    `json:"region"`
	Accepted  time.Time `json:"accepted"`
	// Kind is "send", or "verification" for an identity's verification
	// email.
	Kind string `json:"kind"`

	// Source is v1 Source or v2 FromEmailAddress as given.
	Source string `json:"source,omitempty"`
	// From is the sender's addr-specs, parsed from Source or the raw From
	// header.
	From    []string `json:"from,omitempty"`
	ReplyTo []string `json:"replyTo,omitempty"`
	// ReturnPath is v1 ReturnPath, v2 FeedbackForwardingEmailAddress or the
	// raw Return-Path header.
	ReturnPath string   `json:"returnPath,omitempty"`
	To         []string `json:"to,omitempty"`
	Cc         []string `json:"cc,omitempty"`
	Bcc        []string `json:"bcc,omitempty"`
	// Envelope is the recipients the sandbox check used.
	Envelope []string `json:"envelope"`

	Subject     *SESContent     `json:"subject,omitempty"`
	Text        *SESContent     `json:"text,omitempty"`
	HTML        *SESContent     `json:"html,omitempty"`
	Headers     []SESHeader     `json:"headers,omitempty"`
	Attachments []SESAttachment `json:"attachments,omitempty"`
	// Raw is the raw MIME as received, base64 in JSON.
	Raw []byte `json:"raw,omitempty"`

	Tags []SESTag `json:"tags,omitempty"`
	// Simulator maps each mailbox simulator recipient to its scenario:
	// "success", "bounce", "ooto", "complaint" or "suppressionlist".
	Simulator map[string]string `json:"simulator,omitempty"`
}

// SESContent is a subject or body and its charset.
type SESContent struct {
	Data    string `json:"data"`
	Charset string `json:"charset,omitempty"`
}

// SESHeader is a v2 message header.
type SESHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SESAttachment is a v2 attachment; Content is base64 in JSON.
type SESAttachment struct {
	FileName                string `json:"fileName"`
	ContentType             string `json:"contentType,omitempty"`
	ContentDisposition      string `json:"contentDisposition,omitempty"`
	ContentDescription      string `json:"contentDescription,omitempty"`
	ContentID               string `json:"contentId,omitempty"`
	ContentTransferEncoding string `json:"contentTransferEncoding,omitempty"`
	Content                 []byte `json:"content"`
}

// SESTag is a message tag.
type SESTag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SESMessages returns one page of the SES sent messages q selects, oldest
// first.
func (c *Client) SESMessages(ctx context.Context, q SESMessageQuery) (*SESMessagePage, error) {
	return fetch[SESMessagePage](ctx, c, http.MethodGet, "/ses/messages", q.Values(), nil)
}

// ClearSESMessages deletes the SES sent messages q selects; Limit and
// Cursor are not sent.
func (c *Client) ClearSESMessages(ctx context.Context, q SESMessageQuery) (*SESClearResult, error) {
	v := q.Values()
	v.Del(ParamLimit)
	v.Del(ParamCursor)
	return fetch[SESClearResult](ctx, c, http.MethodDelete, "/ses/messages", v, nil)
}
