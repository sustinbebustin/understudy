package cli

import (
	"bytes"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/config"
	"github.com/sustinbebustin/understudy/internal/egress"
)

// serverConfig hands sns.httpDelivery's list and CA bundle to the server
// whole, so what serve loads is what egress allows.
func TestServeMapsSNSHTTPDelivery(t *testing.T) {
	cfg := loadFile(t, []string{"--sns-http-delivery-allow", "127.0.0.1:*,receiver.default.svc.cluster.local:8080"}, `{}`)
	s := serverConfig(cfg, "test")
	entries := s.SNSHTTPDelivery.Allow.Entries()
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.String())
	}
	if want := []string{"127.0.0.1:*", "receiver.default.svc.cluster.local:8080"}; !slices.Equal(got, want) {
		t.Errorf("server allow-list = %q, want %q", got, want)
	}
	if setting := s.SNSHTTPDelivery.Allow.Setting(); setting != (egress.Setting{Service: "sns", Feature: "httpDelivery"}) {
		t.Errorf("server allow-list setting = %v, want sns.httpDelivery.allow", setting)
	}
	if s.SNSHTTPDelivery.CABundle != nil {
		t.Errorf("server CA bundle = %d certificates with none configured", len(s.SNSHTTPDelivery.CABundle))
	}
}

// An allow-list entry egress refuses stops serve as a usage error (exit 2)
// naming the setting, the entry and the grammar (sns-http-delivery.md 7.2,
// 7.3).
func TestServeRefusesABadAllowListEntry(t *testing.T) {
	var out, errOut bytes.Buffer
	none := func(path string) ([]byte, error) {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	code := Run(t.Context(), []string{"serve", "--sns-http-delivery-allow", "127.0.0.1:8080,169.254.169.254:80"}, Options{Stdout: &out, Stderr: &errOut, ReadFile: none})
	msg := errOut.String()
	for _, want := range []string{"--sns-http-delivery-allow", "entry 1", "169.254.169.254", "instance metadata", "entries are host:port"} {
		if code != admin.ExitUsage || !strings.Contains(msg, want) {
			t.Errorf("serve with a metadata entry = %d, stderr %q; want 2 naming %q", code, msg, want)
		}
	}
	if _, cerr := config.Load([]string{"--sns-http-delivery-allow", "127.0.0.1:8080"}, nil, none); cerr != nil {
		t.Errorf("the good entry alone = %v", cerr)
	}
}
