package server

import (
	"errors"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// sesMessages serves GET and DELETE /ses/messages from the SES store.
type sesMessages struct {
	svc     *ses.Service
	regions aws.Regions
}

var _ serveradmin.SESMessages = sesMessages{}

func (s sesMessages) SESMessages(q admin.SESMessageQuery) (admin.SESMessagePage, *admin.Error) {
	mq, aerr := s.query(q)
	if aerr != nil {
		return admin.SESMessagePage{}, aerr
	}
	mq.Limit, mq.Cursor = q.Limit, q.Cursor
	page, err := s.svc.Messages(mq)
	if err != nil {
		return admin.SESMessagePage{}, sesQueryError(err)
	}
	out := admin.SESMessagePage{Messages: make([]admin.SESMessage, 0, len(page.Messages)), Next: page.Next, Evicted: page.Evicted}
	for i := range page.Messages {
		out.Messages = append(out.Messages, wireSESMessage(&page.Messages[i]))
	}
	return out, nil
}

func (s sesMessages) ClearSESMessages(q admin.SESMessageQuery) (admin.SESClearResult, *admin.Error) {
	mq, aerr := s.query(q)
	if aerr != nil {
		return admin.SESClearResult{}, aerr
	}
	n, err := s.svc.ClearMessages(mq)
	if err != nil {
		return admin.SESClearResult{}, sesQueryError(err)
	}
	return admin.SESClearResult{Deleted: n}, nil
}

// query reads q's filters; an empty account or region selects every one.
func (s sesMessages) query(q admin.SESMessageQuery) (ses.MessageQuery, *admin.Error) {
	out := ses.MessageQuery{MessageID: q.MessageID, From: q.From, To: q.To}
	if q.Account != "" {
		a, err := aws.ParseAccountID(q.Account)
		if err != nil {
			return out, &admin.Error{Code: admin.InvalidRequest, Field: admin.ParamSESAccount, Message: err.Error(), Hint: "leave account out to read every account"}
		}
		out.Account = a
	}
	if q.Region != "" {
		r, err := s.regions.Parse(q.Region)
		if err != nil {
			return out, &admin.Error{Code: admin.InvalidRequest, Field: admin.ParamSESRegion, Message: err.Error(), Hint: "leave region out to read every Region"}
		}
		out.Region = r
	}
	if !q.Since.IsZero() {
		out.Since = clock.FromWire(q.Since)
	}
	return out, nil
}

// sesQueryError maps a store refusal onto the admin vocabulary: a
// *ses.QueryError names its field; anything else is the store failing.
func sesQueryError(err error) *admin.Error {
	if qe, ok := errors.AsType[*ses.QueryError](err); ok {
		return &admin.Error{Code: admin.InvalidRequest, Field: qe.Field, Message: qe.Reason}
	}
	return &admin.Error{Code: admin.Internal, Message: "reading the SES message store failed: " + err.Error()}
}

// wireSESMessage is m in the admin API's JSON form (contract 3.1).
func wireSESMessage(m *ses.Message) admin.SESMessage {
	content := func(c *ses.Content) *admin.SESContent {
		if c == nil {
			return nil
		}
		return &admin.SESContent{Data: c.Data, Charset: c.Charset}
	}
	out := admin.SESMessage{
		MessageID:  m.MessageID,
		API:        string(m.API),
		Operation:  m.Operation,
		RequestID:  string(m.RequestID),
		Account:    m.Scope.Account.String(),
		Region:     m.Scope.Region.String(),
		Accepted:   m.Accepted.Wire(),
		Kind:       string(m.Kind),
		Source:     m.Source,
		From:       m.From,
		ReplyTo:    m.ReplyTo,
		ReturnPath: m.ReturnPath,
		To:         m.To,
		Cc:         m.Cc,
		Bcc:        m.Bcc,
		// Envelope is always a JSON array, empty included.
		Envelope: append([]string{}, m.Envelope...),
		Subject:  content(m.Subject),
		Text:     content(m.Text),
		HTML:     content(m.HTML),
		Raw:      m.Raw,
	}
	for _, h := range m.Headers {
		out.Headers = append(out.Headers, admin.SESHeader{Name: h.Name, Value: h.Value})
	}
	for _, a := range m.Attachments {
		out.Attachments = append(out.Attachments, admin.SESAttachment{
			FileName: a.FileName, ContentType: a.ContentType, ContentDisposition: a.ContentDisposition,
			ContentDescription: a.ContentDescription, ContentID: a.ContentID,
			ContentTransferEncoding: a.ContentTransferEncoding, Content: a.Content,
		})
	}
	for _, t := range m.Tags {
		out.Tags = append(out.Tags, admin.SESTag{Name: t.Name, Value: t.Value})
	}
	if len(m.Simulator) > 0 {
		out.Simulator = make(map[string]string, len(m.Simulator))
		for k, v := range m.Simulator {
			out.Simulator[k] = string(v)
		}
	}
	return out
}
