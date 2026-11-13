package s3_test

import (
	"encoding/xml"
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// TestUnroutedObjectMethodOverHTTP: a request to an object that no S3 route
// matches answers 405 MethodNotAllowed naming the method and the OBJECT
// resource, with Allow listing the methods an object takes, rather than
// reaching an operation or the edge's unknown-operation answer.
func TestUnroutedObjectMethodOverHTTP(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		e := newObjectEdge(k)
		if rec := e.send(http.MethodPut, "/edge", strings.NewReader(""), 0, nil); rec.Code != http.StatusOK {
			t.Fatalf("CreateBucket = %d %s", rec.Code, rec.Body)
		}
		const complete = `<CompleteMultipartUpload xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Part><PartNumber>1</PartNumber><ETag>"x"</ETag></Part></CompleteMultipartUpload>`
		xmlBody := http.Header{"Content-Type": {"application/xml"}}
		for _, tc := range []struct {
			name, method string
		}{
			// AWS-recorded: a POST naming none of uploadId, uploads, restore
			// or select (testdata/parity/s3/R12/14-post-without-upload-id.json).
			{"POST without uploadId", http.MethodPost},
			// inferred: another method no object route takes answers in the
			// same form, naming that method.
			{"PATCH", http.MethodPatch},
		} {
			rec := e.send(tc.method, "/edge/r", strings.NewReader(complete), int64(len(complete)), xmlBody)
			var doc struct {
				XMLName      xml.Name `xml:"Error"`
				Code         string   `xml:"Code"`
				Message      string   `xml:"Message"`
				Method       string   `xml:"Method"`
				ResourceType string   `xml:"ResourceType"`
			}
			if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
				t.Fatalf("%s: body %q does not parse as <Error>: %v", tc.name, rec.Body, err)
			}
			if rec.Code != http.StatusMethodNotAllowed || doc.Code != "MethodNotAllowed" ||
				doc.Message != "The specified method is not allowed against this resource." ||
				doc.Method != tc.method || doc.ResourceType != "OBJECT" {
				t.Fatalf("%s: = %d %s, want 405 MethodNotAllowed %q with Method %s and ResourceType OBJECT",
					tc.name, rec.Code, rec.Body, "The specified method is not allowed against this resource.", tc.method)
			}
			if got := rec.Header().Get("Allow"); got != "HEAD, DELETE, GET, PUT" {
				t.Fatalf("%s: Allow = %q, want %q", tc.name, got, "HEAD, DELETE, GET, PUT")
			}
		}

		// With uploadId the POST is still CompleteMultipartUpload, which
		// answers NoSuchUpload for an upload that does not exist.
		rec := e.send(http.MethodPost, "/edge/r?uploadId=missing", strings.NewReader(complete), int64(len(complete)), xmlBody)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "<Code>NoSuchUpload</Code>") {
			t.Fatalf("CompleteMultipartUpload of a missing upload = %d %s, want 404 NoSuchUpload", rec.Code, rec.Body)
		}
	}, kerneltest.Services(s3api.Service()))
}
