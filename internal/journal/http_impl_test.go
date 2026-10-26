package journal

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/ref"
)

// The class is always written, so a line whose "http" member has none is
// refused rather than read as an exchange of no class.
func TestDecodeRefusesAnHTTPExchangeWithoutAClass(t *testing.T) {
	good := strings.TrimSpace(encodeDetailLine(t, httpDelivery(&HTTPExchange{Status: 503, Class: HTTPRetryable}, Retrying{Next: epoch.Add(time.Minute)})))
	bad := strings.Replace(good, `,"class":"retryable"`, ``, 1)
	if bad == good {
		t.Fatalf("line %s has no class to replace", good)
	}
	var errs []error
	for _, err := range DecodeNDJSON(strings.NewReader(bad + "\n")) {
		errs = append(errs, err)
	}
	if len(errs) != 1 {
		t.Fatalf("decoded %d results, want one error", len(errs))
	}
	if _, ok := errors.AsType[*HTTPClassError](errs[0]); !ok {
		t.Errorf("error = %v, want an *HTTPClassError", errs[0])
	}
}

// An exchange of an undeclared class has no wire form.
func TestEncodeRefusesAnUndeclaredHTTPClass(t *testing.T) {
	var buf bytes.Buffer
	d := httpDelivery(&HTTPExchange{Status: 200, Class: HTTPRefused + 1}, Delivered{})
	err := EncodeNDJSON(&buf, Event{ID: 9, Cause: ref.Cause{Parent: 3, Trace: 1}, At: epoch, Wall: wall0, Scope: scope(), Detail: d})
	if _, ok := errors.AsType[*HTTPClassError](err); !ok {
		t.Errorf("EncodeNDJSON = %v, want an *HTTPClassError", err)
	}
}
