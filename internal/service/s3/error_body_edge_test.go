package s3_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	idsource "github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// An S3 error body is AWS's once the request id and host id values are
// masked: an apostrophe in the message is written as itself, and the
// request id has S3's form, the same in x-amz-request-id, the body's
// RequestId and the journal (testdata/parity/s3/R12/12-lifecycle-expiration-0.json).
// The capture has no XML declaration, a difference understudy keeps
// (docs/guide/differences.md), so the test compares the body after it.
func TestS3ErrorBodyKeepsApostrophesAndTheRequestIDForm(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		send := edgeExchangeAs(k)
		account := aws.DefaultAccount().String()
		if code, _, body := send(account, http.MethodPut, s3Host, "/lifecycled", ""); code != http.StatusOK {
			t.Fatalf("CreateBucket = %d %s, want 200", code, body)
		}
		const step = "12-lifecycle-expiration-0.json"
		recorded := readRecordedResponse(t, "R12", step)
		if !s3RequestIDForm.MatchString(recorded.requestID) {
			t.Fatalf("%s: recorded request id %q does not have the form the test expects, %s", step, recorded.requestID, s3RequestIDForm)
		}
		const lifecycle = `<LifecycleConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Rule><ID>r</ID><Status>Enabled</Status><Filter><Prefix /></Filter><Expiration><Days>0</Days></Expiration></Rule></LifecycleConfiguration>`
		code, header, body := send(account, http.MethodPut, s3Host, "/lifecycled?lifecycle", lifecycle)
		document, declared := strings.CutPrefix(body, `<?xml version="1.0" encoding="UTF-8"?>`+"\n")
		if code != recorded.status || !declared || maskIDs(document) != maskIDs(recorded.body) {
			t.Errorf("PutBucketLifecycleConfiguration with Days 0 = %d\n%s\nwant %d (%s) after the XML declaration\n%s", code, body, recorded.status, step, recorded.body)
		}
		id := header.Get("X-Amz-Request-Id")
		if !s3RequestIDForm.MatchString(id) || !strings.Contains(body, "<RequestId>"+id+"</RequestId>") {
			t.Errorf("x-amz-request-id %q with body %s, want a request id of the recorded form %s in both", id, body, s3RequestIDForm)
		}
		journaled(k, s3api.OpPutBucketLifecycleConfiguration.Descriptor().ID.Name, id)
	}, kerneltest.Services(s3api.Service()))
}

// An S3 200 carries a request id of S3's form too, as every S3 capture's
// x-amz-request-id is (testdata/parity/s3/R06/07-list-objects-v2.json),
// and the journal records the same one.
func TestS3SuccessCarriesTheRequestIDForm(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		send := edgeExchangeAs(k)
		code, header, body := send(aws.DefaultAccount().String(), http.MethodPut, s3Host, "/formed", "")
		if code != http.StatusOK {
			t.Fatalf("CreateBucket = %d %s, want 200", code, body)
		}
		id := header.Get("X-Amz-Request-Id")
		if !s3RequestIDForm.MatchString(id) {
			t.Errorf("CreateBucket x-amz-request-id = %q, want the recorded form %s", id, s3RequestIDForm)
		}
		journaled(k, s3api.OpCreateBucket.Descriptor().ID.Name, id)
	}, kerneltest.Services(s3api.Service()))
}

// journaled fails the test unless the journal holds one API call of the
// S3 operation op under request id id.
func journaled(k *kerneltest.K, op, id string) {
	k.Helper()
	events := k.Journal.Query(journal.Filter{Kind: journal.KindAPICall, RequestID: idsource.RequestID(id)}).Events
	if len(events) != 1 {
		k.Errorf("journal holds %d API calls under request id %q, want the one the client saw", len(events), id)
		return
	}
	if call, ok := events[0].Detail.(journal.APICall); !ok || call.Op.Name != op {
		k.Errorf("request id %q was journaled as %+v, want the %s call", id, events[0].Detail, op)
	}
}
