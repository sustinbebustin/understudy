package parity

import (
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
)

// Client is what sent a recorded request (testdata/README.md, "Parity
// fixture format").
type Client string

// The recording clients.
const (
	AWSCLI     Client = "aws-cli"
	Boto3      Client = "boto3"
	Curl       Client = "curl"
	HTTPClient Client = "http.client"
	Socket     Client = "socket"
)

// Fixture is one recorded step, testdata/parity/<svc>/<item>/<step>.json.
type Fixture struct {
	Step   string
	Client Client
	// Exit is the client's exit status, absent for curl, http.client and
	// socket steps.
	Exit      *int
	Error     string
	Stdout    string
	Note      string
	Exchanges []Exchange
}

// Exchange is one HTTP exchange of a step, retries included.
type Exchange struct {
	// Request is nil where the capture recorded none.
	Request *Request
	// Response is nil where the capture recorded none.
	Response *Response
	// Interim are curl's 100 Continue answers.
	Interim []Response
}

// Request is a recorded request.
type Request struct {
	Method string
	// URL is absolute, or only the request target where the capture did
	// not record the host.
	URL     string
	Headers []Header
	Body    Body
}

// Response is a recorded response, or understudy's answer to a replayed
// request.
type Response struct {
	// Status is 0 where the capture recorded none.
	Status  int
	Reason  string
	Headers []Header
	Body    Body
}

// Header is one header, in recorded order, duplicates kept.
type Header struct {
	Name, Value string
}

// Body is a recorded body: at most one of the fixture's body members.
//
//sumtype:decl
type Body interface{ isBody() }

// NoBody is an empty request body or a response without one.
type NoBody struct{}

// TextBody is the exact bytes as UTF-8 text ("body").
type TextBody struct {
	Text string
}

// BinaryBody is the exact bytes, which are not UTF-8 ("body_base64").
type BinaryBody struct {
	Bytes []byte
}

// OmittedBody is the length and digest of a body over 64 KiB
// ("body_omitted").
type OmittedBody struct {
	Bytes  int64
	SHA256 string
}

// UnrecordedBody is a body the capture kept no bytes of
// ("body_unrecorded"); Bytes is nil when the length is unknown.
type UnrecordedBody struct {
	Bytes *int64
}

func (NoBody) isBody()         {}
func (TextBody) isBody()       {}
func (BinaryBody) isBody()     {}
func (OmittedBody) isBody()    {}
func (UnrecordedBody) isBody() {}

// FixtureError reports a fixture that does not follow the format.
type FixtureError struct {
	// Path is the fixture path under testdata/parity, empty when the
	// fixture was parsed from bytes alone.
	Path   string
	Reason string
}

func (e *FixtureError) Error() string {
	if e.Path == "" {
		return "parity fixture: " + e.Reason
	}
	return fmt.Sprintf("testdata/parity/%s: %s", e.Path, e.Reason)
}

// ParseFixture reads one fixture strictly: an unknown member, a member
// named twice, two body members, a missing step, client or exchanges, or a
// request without its method or url is a *FixtureError. The format
// (testdata/README.md, "Parity fixture format") leaves out that a response
// and its status can be absent; the recordings have both cases, so they
// parse as a nil Response and a zero Status.
func ParseFixture(data []byte) (Fixture, error) {
	var doc fixtureDoc
	if err := json.Unmarshal(data, &doc, json.RejectUnknownMembers(true)); err != nil {
		return Fixture{}, &FixtureError{Reason: "not a fixture document: " + err.Error()}
	}
	f, reason := doc.fixture()
	if reason != "" {
		return Fixture{}, &FixtureError{Reason: reason}
	}
	return f, nil
}

// fixtureDoc is a fixture as the file spells it.
type fixtureDoc struct {
	Step      *string        `json:"step"`
	Client    *string        `json:"client"`
	Exit      *int           `json:"exit"`
	Error     string         `json:"error"`
	Stdout    string         `json:"stdout"`
	Note      string         `json:"note"`
	Exchanges *[]exchangeDoc `json:"exchanges"`
}

type exchangeDoc struct {
	Request  *requestDoc   `json:"request"`
	Response *responseDoc  `json:"response"`
	Interim  []responseDoc `json:"interim_responses"`
}

type requestDoc struct {
	Method  *string    `json:"method"`
	URL     *string    `json:"url"`
	Headers [][]string `json:"headers"`
	bodyDoc `json:",inline"`
}

type responseDoc struct {
	Status  int        `json:"status"`
	Reason  string     `json:"reason"`
	Headers [][]string `json:"headers"`
	bodyDoc `json:",inline"`
}

// bodyDoc holds the body members, at most one of which may be present.
type bodyDoc struct {
	Body       *string        `json:"body"`
	Base64     *string        `json:"body_base64"`
	Omitted    *omittedDoc    `json:"body_omitted"`
	Unrecorded *unrecordedDoc `json:"body_unrecorded"`
}

type omittedDoc struct {
	Bytes  *int64  `json:"bytes"`
	SHA256 *string `json:"sha256"`
}

type unrecordedDoc struct {
	// Bytes is a length or null; absent is an error, so it is read raw.
	Bytes jsontext.Value `json:"bytes"`
}

// fixture converts d, or says why it is not a fixture.
func (d fixtureDoc) fixture() (f Fixture, reason string) {
	switch {
	case d.Step == nil || *d.Step == "":
		return Fixture{}, "the step member is missing"
	case d.Client == nil:
		return Fixture{}, "the client member is missing"
	case !slices.Contains([]Client{AWSCLI, Boto3, Curl, HTTPClient, Socket}, Client(*d.Client)):
		return Fixture{}, fmt.Sprintf("client %q is not one of aws-cli, boto3, curl, http.client, socket", *d.Client)
	case d.Exchanges == nil:
		return Fixture{}, "the exchanges member is missing (an empty list when the client sent nothing)"
	}
	f = Fixture{Step: *d.Step, Client: Client(*d.Client), Exit: d.Exit, Error: d.Error, Stdout: d.Stdout, Note: d.Note}
	f.Exchanges = make([]Exchange, 0, len(*d.Exchanges))
	for i, x := range *d.Exchanges {
		e, reason := x.exchange()
		if reason != "" {
			return Fixture{}, fmt.Sprintf("exchanges[%d]: %s", i, reason)
		}
		f.Exchanges = append(f.Exchanges, e)
	}
	return f, ""
}

func (x exchangeDoc) exchange() (e Exchange, reason string) {
	if x.Request != nil {
		r, reason := x.Request.request()
		if reason != "" {
			return Exchange{}, "request: " + reason
		}
		e.Request = &r
	}
	if x.Response != nil {
		r, reason := x.Response.response()
		if reason != "" {
			return Exchange{}, "response: " + reason
		}
		e.Response = &r
	}
	for i, ir := range x.Interim {
		r, reason := ir.response()
		if reason != "" {
			return Exchange{}, fmt.Sprintf("interim_responses[%d]: %s", i, reason)
		}
		e.Interim = append(e.Interim, r)
	}
	return e, ""
}

func (r requestDoc) request() (req Request, reason string) {
	if r.Method == nil || r.URL == nil {
		return Request{}, "method and url are required"
	}
	hs, reason := headers(r.Headers)
	if reason != "" {
		return Request{}, reason
	}
	b, reason := r.body()
	if reason != "" {
		return Request{}, reason
	}
	return Request{Method: *r.Method, URL: *r.URL, Headers: hs, Body: b}, ""
}

func (r responseDoc) response() (resp Response, reason string) {
	hs, reason := headers(r.Headers)
	if reason != "" {
		return Response{}, reason
	}
	b, reason := r.body()
	if reason != "" {
		return Response{}, reason
	}
	return Response{Status: r.Status, Reason: r.Reason, Headers: hs, Body: b}, ""
}

func headers(pairs [][]string) (hs []Header, reason string) {
	hs = make([]Header, 0, len(pairs))
	for i, p := range pairs {
		if len(p) != 2 {
			return nil, fmt.Sprintf("headers[%d] has %d strings; want [name, value]", i, len(p))
		}
		hs = append(hs, Header{Name: p[0], Value: p[1]})
	}
	return hs, ""
}

func (b bodyDoc) body() (body Body, reason string) {
	present := 0
	for _, set := range []bool{b.Body != nil, b.Base64 != nil, b.Omitted != nil, b.Unrecorded != nil} {
		if set {
			present++
		}
	}
	switch {
	case present > 1:
		return nil, "holds more than one of body, body_base64, body_omitted and body_unrecorded"
	case b.Body != nil:
		return TextBody{Text: *b.Body}, ""
	case b.Base64 != nil:
		bs, err := base64.StdEncoding.DecodeString(*b.Base64)
		if err != nil {
			return nil, "body_base64 is not base64: " + err.Error()
		}
		return BinaryBody{Bytes: bs}, ""
	case b.Omitted != nil:
		if b.Omitted.Bytes == nil || b.Omitted.SHA256 == nil {
			return nil, "body_omitted needs bytes and sha256"
		}
		return OmittedBody{Bytes: *b.Omitted.Bytes, SHA256: *b.Omitted.SHA256}, ""
	case b.Unrecorded != nil:
		return b.Unrecorded.body()
	}
	return NoBody{}, ""
}

func (u unrecordedDoc) body() (body Body, reason string) {
	switch u.Bytes.Kind() {
	case jsontext.KindNull:
		return UnrecordedBody{}, ""
	case jsontext.KindNumber:
		n, err := strconv.ParseInt(string(u.Bytes), 10, 64)
		if err != nil || n < 0 {
			return nil, fmt.Sprintf("body_unrecorded bytes %s is not a length", u.Bytes)
		}
		return UnrecordedBody{Bytes: &n}, ""
	case jsontext.KindInvalid, jsontext.KindFalse, jsontext.KindTrue, jsontext.KindString,
		jsontext.KindBeginObject, jsontext.KindEndObject, jsontext.KindBeginArray, jsontext.KindEndArray:
	}
	return nil, "body_unrecorded needs bytes: a length, or null when unknown"
}
