package parity

import "fmt"

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

// ParseFixture reads one fixture strictly. Not implemented yet: it refuses
// every fixture.
func ParseFixture([]byte) (Fixture, error) {
	return Fixture{}, &FixtureError{Reason: notImplemented("ParseFixture").Error()}
}
