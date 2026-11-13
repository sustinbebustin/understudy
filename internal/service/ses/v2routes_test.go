package ses_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// wire sends a request signed for ses, the signing name both SES APIs
// share (ADR 0019), through an edge on the main listener, and answers the
// status and body.
func (h *harness) wire(method, target, body string) (status int, answer string) {
	h.Helper()
	admin := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		h.Errorf("the admin API received %s %s", r.Method, r.URL)
	})
	e, err := edge.New(edge.Options{
		Kernel: h.Kernel, Registry: h.Kernel.Registry(), Identity: h.Identity, Regions: aws.KnownRegions(),
		Codecs: server.Codecs(), S3BaseDomains: []string{"localhost"}, Listener: ref.ListenerMain, Admin: admin,
	})
	if err != nil {
		h.Fatal(err)
	}
	r := httptest.NewRequestWithContext(h.Context(), method, "http://localhost:4566"+target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	auth.Sign(r, []byte(body), auth.Signer{AccessKeyID: h.Scope.Account.String(), SecretAccessKey: "secret", Region: h.Scope.Region, Service: "ses"}, h.Clock.Wall())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)
	got, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		h.Fatal(err)
	}
	return rec.Code, string(got)
}

// identityPage is a ListEmailIdentities answer as the wire carries it.
type identityPage struct {
	EmailIdentities []struct{ IdentityName string }
	NextToken       *string
}

// pages lists every identity one page at a time through next, which sends
// the request for a page given the previous page's NextToken ("" first).
func (h *harness) pages(form string, next func(token string) (int, string)) [][]string {
	h.Helper()
	var out [][]string
	token := ""
	for range 10 {
		status, body := next(token)
		if status != http.StatusOK {
			h.Fatalf("%s ListEmailIdentities = %d %s", form, status, body)
		}
		var p identityPage
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			h.Fatalf("%s ListEmailIdentities body %s: %v", form, body, err)
		}
		names := make([]string, 0, len(p.EmailIdentities))
		for _, e := range p.EmailIdentities {
			names = append(names, e.IdentityName)
		}
		out = append(out, names)
		if p.NextToken == nil {
			return out
		}
		token = *p.NextToken
	}
	h.Fatalf("%s ListEmailIdentities pages without end: %q", form, out)
	return nil
}

// The GET /v2/email/identities binding, which SDKs generated before
// 2026-09-29 send with NextToken and PageSize in the query, answers the
// same pages as the POST /v2/email/list-identities binding (SES-14, ses.md
// 3.2 and 5.4).
func TestListEmailIdentitiesByTheGETBinding(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		h.createIdentity("pending@example.com")
		get := h.pages("GET", func(token string) (int, string) {
			q := url.Values{"PageSize": {"1"}}
			if token != "" {
				q.Set("NextToken", token)
			}
			return h.wire(http.MethodGet, "/v2/email/identities?"+q.Encode(), "")
		})
		post := h.pages("POST", func(token string) (int, string) {
			in := map[string]any{"PageSize": 1}
			if token != "" {
				in["NextToken"] = token
			}
			body, err := json.Marshal(in)
			if err != nil {
				h.Fatal(err)
			}
			return h.wire(http.MethodPost, "/v2/email/list-identities", string(body))
		})
		if len(get) != 3 || !slices.EqualFunc(get, post, slices.Equal[[]string]) {
			h.Errorf("GET pages = %q, POST pages = %q; want the same three one-identity pages", get, post)
		}
	})
}

// POST /v2/email/identities stays CreateEmailIdentity beside the GET
// binding of ListEmailIdentities on the same path (ses.md 11.6).
func TestCreateEmailIdentityKeepsThePOSTBinding(t *testing.T) {
	run(t, func(h *harness) {
		status, body := h.wire(http.MethodPost, "/v2/email/identities", `{"EmailIdentity":"new@example.com"}`)
		if status != http.StatusOK || !strings.Contains(body, `"IdentityType":"EMAIL_ADDRESS"`) {
			h.Fatalf("POST /v2/email/identities = %d %s, want CreateEmailIdentity's answer", status, body)
		}
		if got := h.getIdentity("new@example.com"); got.IdentityType == nil {
			h.Errorf("GetEmailIdentity after the POST = %+v, want the created identity", got)
		}
	})
}

// setPages lists every configuration set one page at a time through next,
// as pages does for identities.
func (h *harness) setPages(form string, next func(token string) (int, string)) [][]string {
	h.Helper()
	var out [][]string
	token := ""
	for range 10 {
		status, body := next(token)
		if status != http.StatusOK {
			h.Fatalf("%s ListConfigurationSets = %d %s", form, status, body)
		}
		var p struct {
			ConfigurationSets []string
			NextToken         *string
		}
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			h.Fatalf("%s ListConfigurationSets body %s: %v", form, body, err)
		}
		out = append(out, p.ConfigurationSets)
		if p.NextToken == nil {
			return out
		}
		token = *p.NextToken
	}
	h.Fatalf("%s ListConfigurationSets pages without end: %q", form, out)
	return nil
}

// GET /v2/email/configuration-sets, the binding of ListConfigurationSets in
// the model before 2026-09-29 (ses.md 3.2), and POST
// /v2/email/list-configuration-sets list the same sets, whichever API
// created them (one store, switch ses.store.shared-v1-v2).
func TestListConfigurationSetsByBothBindings(t *testing.T) {
	run(t, func(h *harness) {
		h.createSet("by-v1")
		if status, body := h.wire(http.MethodPost, "/v2/email/configuration-sets", `{"ConfigurationSetName":"by-v2"}`); status != http.StatusOK {
			h.Fatalf("POST /v2/email/configuration-sets = %d %s", status, body)
		}
		get := h.setPages("GET", func(token string) (int, string) {
			q := url.Values{"PageSize": {"1"}}
			if token != "" {
				q.Set("NextToken", token)
			}
			return h.wire(http.MethodGet, "/v2/email/configuration-sets?"+q.Encode(), "")
		})
		post := h.setPages("POST", func(token string) (int, string) {
			in := map[string]any{"PageSize": 1}
			if token != "" {
				in["NextToken"] = token
			}
			body, err := json.Marshal(in)
			if err != nil {
				h.Fatal(err)
			}
			return h.wire(http.MethodPost, "/v2/email/list-configuration-sets", string(body))
		})
		want := [][]string{{"by-v1"}, {"by-v2"}}
		if !slices.EqualFunc(get, want, slices.Equal[[]string]) || !slices.EqualFunc(post, want, slices.Equal[[]string]) {
			h.Errorf("GET pages = %q, POST pages = %q; want %q from both", get, post, want)
		}
	})
}
