package edge_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/journal"
)

// A Kinesis call in CBOR is journaled as Unclaimed, its last reason naming
// CBOR at rule 6 (contract 4: "journaled as journal.Unclaimed with one
// reason naming CBOR"; kinesis.md 13.4 item 2), and answered 501 in CBOR
// whatever service the edge routes, since no Kinesis service is needed to
// refuse it. A JSON call with the same target is not refused here.
func TestKinesisCBORIsJournaledAsUnclaimed(t *testing.T) {
	f := s3Front(t)
	r := request(t, http.MethodPost, "/", jsonHeader("Kinesis_20131202.ListStreams", "application/x-amz-cbor-1.1; charset=utf-8"), "\xa0", "kinesis")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotImplemented || rec.Header().Get("Content-Type") != "application/x-amz-cbor-1.1" {
		t.Errorf("answer = %d %q, want 501 application/x-amz-cbor-1.1", rec.Code, rec.Header().Get("Content-Type"))
	}
	if got := f.lastReason(t); !strings.HasPrefix(got, "rule 6: ") || !strings.Contains(got, "CBOR") {
		t.Errorf("last reason = %q, want rule 6 naming CBOR", got)
	}

	r = request(t, http.MethodPost, "/", jsonHeader("Kinesis_20131202.ListStreams", "application/x-amz-json-1.1"), "{}", "kinesis")
	rec = httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	if rec.Code == http.StatusNotImplemented && rec.Header().Get("Content-Type") == "application/x-amz-cbor-1.1" {
		t.Error("a JSON Kinesis call got the CBOR refusal")
	}
}

// An HTTP/2 preface is journaled as Unclaimed with the diagnostic as its
// reason, since the client cannot parse the HTTP/1.1 answer and the
// journal is where it is read (contract 4; kinesis.md 13.9).
func TestHTTP2PrefaceReasonCarriesTheDiagnostic(t *testing.T) {
	f := s3Front(t)
	r := request(t, "PRI", "/", nil, "", "")
	r.RequestURI = "*"
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rec.Code)
	}
	if events := f.journal.Query(journal.Filter{Kind: journal.KindUnclaimed}).Events; len(events) != 1 {
		t.Errorf("journaled %d Unclaimed events, want 1", len(events))
	}
	if got := f.lastReason(t); got != "rule 9: "+rec.Body.String() {
		t.Errorf("last reason = %q, want rule 9 with the answer's text %q", got, rec.Body.String())
	}
}
