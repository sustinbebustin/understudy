package httpevent_test

import (
	"fmt"
	"net/http"
	"testing"
)

// A bodyless request's body and isBase64Encoded follow the caller's
// apigateway.proxy.unrecorded choices (APIGW1-16), each flippable; a
// request with a body is unaffected (aws-recorded, apigateway-rest.md 5.1).
func TestEventV1RESTBodylessChoices(t *testing.T) {
	for _, empty := range []bool{false, true} {
		for _, encoded := range []bool{false, true} {
			t.Run(fmt.Sprintf("empty=%v,base64=%v", empty, encoded), func(t *testing.T) {
				f := restFront()
				f.BodylessEmpty, f.BodylessBase64 = empty, encoded
				r := restRequest()
				r.Method, r.Body = http.MethodPost, nil
				ev := restEvent(t, r, restContext(), f)
				var want any
				if empty {
					want = ""
				}
				if ev["body"] != want || ev["isBase64Encoded"] != encoded {
					t.Errorf("bodyless body = %#v, isBase64Encoded %v; want %#v, %v", ev["body"], ev["isBase64Encoded"], want, encoded)
				}
				r.Body = []byte("x")
				ev = restEvent(t, r, restContext(), f)
				if ev["body"] != "x" || ev["isBase64Encoded"] != false {
					t.Errorf("body x = %#v, isBase64Encoded %v; want \"x\", false", ev["body"], ev["isBase64Encoded"])
				}
			})
		}
	}
}
