package lambda_test

import (
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool/pooltest"
)

// runAPI runs fn against the Lambda API behind an edge with the server's
// codecs that parses signatures without verifying them.
func runAPI(t *testing.T, fn func(h *urlHarness)) {
	t.Helper()
	runURLsBehind(t, pooltest.Echo, func(o *edge.Options) { o.Codecs = server.Codecs() }, fn)
}

// lambdaPost is a POST of path on the Lambda API with header, whose body
// is length bytes from body, signed for lambda by the account's root.
func (h *urlHarness) lambdaPost(path string, header http.Header, body io.Reader, length int64) *http.Request {
	h.Helper()
	creds, err := h.Identity.MintLongTerm(identity.Root{Account: aws.DefaultAccount()})
	if err != nil {
		h.Fatal(err)
	}
	r := h.request(http.MethodPost, "http://localhost:4566"+path, "")
	r.Body = io.NopCloser(io.LimitReader(body, length))
	r.ContentLength = length
	maps.Copy(r.Header, header)
	auth.Sign(r, nil, auth.Signer{
		AccessKeyID: string(creds.AccessKeyID), SecretAccessKey: string(creds.SecretAccessKey),
		Region: aws.DefaultRegion(), Service: "lambda",
	}, h.Clock.Wall())
	return r
}

// expectTooLarge checks a Lambda 413 against the recorded wire: status 413,
// x-amzn-ErrorType RequestEntityTooLargeException and the text under
// Message, with no Type.
func expectTooLarge(tb testing.TB, rec *httptest.ResponseRecorder, message string) {
	tb.Helper()
	want := `{"Message":"` + message + `"}`
	errorType := rec.Header().Get("X-Amzn-Errortype")
	if rec.Code != http.StatusRequestEntityTooLarge || errorType != "RequestEntityTooLargeException" || rec.Body.String() != want {
		tb.Errorf("answer %d x-amzn-ErrorType %q %s\nwant   413 x-amzn-ErrorType \"RequestEntityTooLargeException\" %s",
			rec.Code, errorType, rec.Body, want)
	}
}

// An Event Invoke over 1 MB names the payload's size
// (testdata/parity/lambda/R13/04-event-over-1mb.json, whose payload is
// 1048688 bytes).
func TestEventInvokeOverTheLimitAnswersTheRecordedWire(t *testing.T) {
	runAPI(t, func(h *urlHarness) {
		h.createFunction("fn")
		const size = 1_048_688
		payload := `"` + strings.Repeat("x", size-2) + `"`
		header := http.Header{"X-Amz-Invocation-Type": {"Event"}}
		rec := h.send(h.lambdaPost("/2015-03-31/functions/fn/invocations", header, strings.NewReader(payload), size))
		expectTooLarge(t, rec, "1048688 byte payload is too large for the Event invocation type (limit 1048576 bytes)")
		if n := len(h.started()); n != 0 {
			t.Errorf("the refused invocation started %d containers", n)
		}
	})
}

// PublishLayerVersion's request, the base64 body rather than the archive,
// is capped at 70167211 bytes
// (testdata/parity/lambda/LDL-14/03-publish-53-zipped.json). CreateFunction's
// cap answers in the same form with its own name (lambda-api.md 2.1, a
// third-party recording); its key and status are inferred from LDL-14.
func TestRequestsOverTheCapAnswerTheRecordedWire(t *testing.T) {
	runAPI(t, func(h *urlHarness) {
		json := http.Header{"Content-Type": {"application/json"}}
		for _, c := range []struct {
			path, op string
		}{
			{"/2018-10-31/layers/big/versions", "PublishLayerVersion"},
			{"/2015-03-31/functions", "CreateFunction"},
		} {
			rec := h.send(h.lambdaPost(c.path, json, zeros{}, 70_167_212))
			expectTooLarge(t, rec, "Request must be smaller than 70167211 bytes for the "+c.op+" operation")
		}
	})
}
