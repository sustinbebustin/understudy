package admin_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/coverage"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
)

// switchService is a wired service that declares one fidelity switch.
type switchService struct{ lateService }

func (*switchService) Switches() []fidelity.Entry {
	return []fidelity.Entry{{Name: "late.order", Value: "fifo", Label: evidence.Inferred, Ref: "research.md#1"}}
}

// GET /coverage answers JSON by default and on format=json, Markdown on
// format=markdown, and refuses any other format naming the ones it knows.
// The report carries the built-in baseline and the wired services'
// switches, and points to docs/coverage.md for the overlays.
func TestCoverage(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handlerWith(k, func(o *serveradmin.Options) { o.Services = []serveradmin.Service{&switchService{}} })
		for _, path := range []string{"/coverage", "/coverage?format=json"} {
			rec := serve(k, h, http.MethodGet, admin.Prefix+path)
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
				k.Fatalf("GET %s = %d %q: %s", path, rec.Code, rec.Header().Get("Content-Type"), rec.Body)
			}
			r, err := coverage.ParseJSON(rec.Body.Bytes())
			if err != nil {
				k.Fatalf("GET %s: %v", path, err)
			}
			if len(r.Protocols) == 0 || len(r.Ungenerated) == 0 {
				k.Errorf("GET %s: protocols %+v, ungenerated %+v; want the baseline and ungenerated cases built in", path, r.Protocols, r.Ungenerated)
			}
			if want := []coverage.Switch{{Name: "late.order", Value: "fifo", Label: evidence.Inferred, Ref: "research.md#1"}}; !reflect.DeepEqual(r.Switches, want) {
				k.Errorf("GET %s: switches = %+v, want %+v", path, r.Switches, want)
			}
			if _, ok := r.Overlays.(coverage.OverlaysNotCarried); !ok {
				k.Errorf("GET %s: overlays = %#v, want not carried", path, r.Overlays)
			}
		}

		rec := serve(k, h, http.MethodGet, admin.Prefix+"/coverage?format=markdown")
		md := rec.Body.String()
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/markdown; charset=utf-8" {
			k.Fatalf("GET format=markdown = %d %q: %s", rec.Code, rec.Header().Get("Content-Type"), md)
		}
		for _, want := range []string{"## Protocol compliance", "## Operations", "### Fidelity switches", "`late.order`", "docs/coverage.md"} {
			if !strings.Contains(md, want) {
				k.Errorf("Markdown lacks %q", want)
			}
		}

		rec = serve(k, h, http.MethodGet, admin.Prefix+"/coverage?format=xml")
		var env admin.Envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || rec.Code != http.StatusBadRequest || env.Error == nil {
			k.Fatalf("GET format=xml = %d %s (%v)", rec.Code, rec.Body, err)
		}
		if e := env.Error; e.Code != admin.InvalidRequest || e.Field != "format" || !reflect.DeepEqual(e.Known, []string{"json", "markdown"}) {
			k.Errorf("GET format=xml error = %+v, want invalid_request on format with known [json markdown]", e)
		}
	})
}
