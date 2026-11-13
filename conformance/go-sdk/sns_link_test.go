package gosdk_test

import (
	"net/http"
	"strings"
	"testing"
)

// linkGet sends a GET of link through the link client; an unparsable link
// fails as the client's refusals do, with an error that names it.
func linkGet(t *testing.T, link string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, link, http.NoBody)
	if err != nil {
		return nil, err
	}
	return linkClient(t).Do(req)
}

// The link client refuses a link off loopback before any request goes out,
// so a wrong SubscribeURL can never reach another host (8.8).
func TestLinkClientRefusesNonLoopback(t *testing.T) {
	t.Parallel()
	for _, link := range []string{
		"https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription",
		"http://example.com:8080/x",
		"http://10.0.0.1:8080/x",
		"http://notlocalhost:8080/x",
		"http://localhost.example.com/x",
		"::not a url",
	} {
		resp, err := linkGet(t, link)
		if err == nil {
			_ = resp.Body.Close()
			t.Errorf("GET %s succeeded, want a refusal", link)
			continue
		}
		if !strings.Contains(err.Error(), link) {
			t.Errorf("GET %s refused with %q, want the error to name the link", link, err)
		}
	}
}

func TestLinkClientAllowsLoopback(t *testing.T) {
	t.Parallel()
	rcv := newHookReceiver(t)
	for _, link := range []string{rcv.URL + "/x", strings.Replace(rcv.URL, "127.0.0.1", "localhost", 1) + "/x"} {
		resp, err := linkGet(t, link)
		if err != nil {
			t.Errorf("GET %s: %v", link, err)
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", link, resp.StatusCode)
		}
	}
}
