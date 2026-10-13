package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The health endpoint's model pin is the vendored commit.
func TestModelPinIsTheVendoredCommit(t *testing.T) {
	data, err := os.ReadFile("../../third_party/api-models-aws/UPSTREAM.json")
	if err != nil {
		t.Fatal(err)
	}
	var upstream struct {
		Commit string `json:"commit"`
	}
	if err := json.Unmarshal(data, &upstream); err != nil {
		t.Fatal(err)
	}
	if want := "api-models-aws@" + upstream.Commit; modelPin != want {
		t.Errorf("modelPin = %q, want %q", modelPin, want)
	}
}

// A registered service whose state no collected view holds is refused at
// startup, rather than silently missing from snapshots and resets; a full
// wiring collects every one.
func TestRegisteredServicesMustBeCollected(t *testing.T) {
	cat := newCatalogue()
	reg, err := registry.New(cat.descs...)
	if err != nil {
		t.Fatal(err)
	}
	err = checkCollected(reg, nil)
	uncollected, ok := errors.AsType[*UncollectedServiceError](err)
	if !ok || len(uncollected.Services) != len(cat.descs) {
		t.Fatalf("checkCollected with nothing collected = %v, want every one of the %d services named", err, len(cat.descs))
	}
	w, err := wire(Config{Docker: DockerDisabled{}}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("wire = %v", err)
	}
	if err := w.close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// The container listener's front door shares the ports the main
// listener's serves, so a page understudy served on the main port (an S3
// object, a function URL response) is refused there too, and a
// developer's own app on another loopback port is not.
func TestContainerListenerRefusesPagesOnTheMainPort(t *testing.T) {
	w, err := wire(Config{Docker: DockerDisabled{}}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("wire = %v", err)
	}
	t.Cleanup(func() {
		if err := w.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	w.edge.Server(nil).BaseContext(ln) // what Serve does first on the main listener
	_, mainPort, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	region, err := aws.KnownRegions().Parse("us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	for origin, want := range map[string]int{
		"http://localhost:" + mainPort: http.StatusForbidden,
		"http://localhost:3000":        http.StatusOK,
	} {
		const body = `{}`
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://127.0.0.1:1/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-amz-json-1.0")
		r.Header.Set("X-Amz-Target", "AmazonSQS.ListQueues")
		r.Header.Set("Origin", origin)
		auth.Sign(r, []byte(body), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: region, Service: "sqs"}, time.Now())
		rec := httptest.NewRecorder()
		w.containerEdge.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Errorf("container listener ListQueues from a page on %s = %d %s, want %d", origin, rec.Code, rec.Body, want)
		}
	}
}

// A wiring that fails after the journal opened its file closes the
// journal: an event recorded afterwards finds the file closed.
func TestFailedWiringClosesTheJournal(t *testing.T) {
	cfg := Config{
		Journal: Journal{File: filepath.Join(t.TempDir(), "journal.ndjson")},
		// A rule without a phase is one the fault store refuses, after the
		// journal is open.
		Faults: []fault.Rule{{ID: "no-phase", Action: fault.Throttle{}, Trigger: fault.Always{}}},
		Docker: DockerDisabled{},
	}
	w, err := wire(cfg, slog.New(slog.DiscardHandler))
	if se, ok := errors.AsType[*SettingError](err); !ok || !strings.HasPrefix(se.Setting, "faults[0]") {
		t.Fatalf("wire with an invalid fault rule = %v, want a SettingError for faults[0]", err)
	}
	if _, err := w.journal.Append(journal.Event{Detail: journal.TimerFired{Key: clock.TimerKey{Kind: "test.probe", ID: "1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := w.journal.FileErr(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("journal file after a failed wiring: %v, want it closed", err)
	}
}

// A configured rule naming an error the operation never sends is refused
// with a hint that lists the errors it does send by the short names
// action.shape takes, as the admin API lists them.
func TestConfiguredRuleHintListsShortNames(t *testing.T) {
	cfg := Config{
		Faults: []fault.Rule{{
			ID:      "wrong-shape",
			Match:   fault.Match{Operations: []ref.OperationID{{Service: "sqs", Name: "SendMessage"}}},
			Phase:   fault.Request,
			Action:  fault.Error{Shape: "NoSuchThing"},
			Trigger: fault.Always{},
		}},
		Docker: DockerDisabled{},
	}
	_, err := wire(cfg, slog.New(slog.DiscardHandler))
	se, ok := errors.AsType[*SettingError](err)
	if !ok || se.Setting != "faults[0].action.shape" {
		t.Fatalf("wire with a rule injecting NoSuchThing on SendMessage = %v, want a SettingError for faults[0].action.shape", err)
	}
	if !strings.Contains(se.Hint, "QueueDoesNotExist") || strings.Contains(se.Hint, "#") {
		t.Errorf("hint = %q, want the short names of the errors SendMessage sends", se.Hint)
	}
}

// A static key the identity store refuses is a setting error naming its
// accounts.keys entry, and its secret appears nowhere in the error.
func TestRefusedStaticKeyNamesItsEntry(t *testing.T) {
	acct, err := aws.ParseAccountID("222222222222")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		StaticKeys: []StaticKey{
			{AccessKeyID: "CIKEY0000000000001", Secret: "first-secret", Account: acct},
			{AccessKeyID: "CIKEY0000000000001", Secret: "second-secret", Account: acct},
		},
		Docker: DockerDisabled{},
	}
	_, err = wire(cfg, slog.New(slog.DiscardHandler))
	se, ok := errors.AsType[*SettingError](err)
	if !ok || se.Setting != "accounts.keys[1]" {
		t.Fatalf("wire with a key listed twice = %v, want a SettingError for accounts.keys[1]", err)
	}
	if strings.Contains(err.Error(), "secret-") {
		t.Errorf("the error names a secret: %v", err)
	}
}

// A static key's secret shows nowhere a Config is rendered: fmt's verbs,
// slog's text and JSON handlers, with the key alone or in a Config, and
// JSON encoding. The key id shows, so the check sees the key rendered.
func TestStaticKeySecretIsRedacted(t *testing.T) {
	acct, err := aws.ParseAccountID("222222222222")
	if err != nil {
		t.Fatal(err)
	}
	key := StaticKey{AccessKeyID: "CIKEY0000000000001", Secret: "static-secret-value", Account: acct}
	cfg := Config{StaticKeys: []StaticKey{key}}
	outputs := make([]string, 0, 13)
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		outputs = append(outputs, fmt.Sprintf(verb, key), fmt.Sprintf(verb, cfg.StaticKeys))
	}
	for _, handler := range []func(*bytes.Buffer) slog.Handler{
		func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
	} {
		for _, attr := range []slog.Attr{slog.Any("key", key), slog.Any("keys", cfg.StaticKeys)} {
			var b bytes.Buffer
			slog.New(handler(&b)).Info("m", attr)
			outputs = append(outputs, b.String())
		}
	}
	encoded, err := json.Marshal(cfg.StaticKeys)
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, string(encoded))
	for _, out := range outputs {
		if strings.Contains(out, key.Secret) || !strings.Contains(out, key.AccessKeyID) {
			t.Errorf("rendered as %s; want the key id without the secret", out)
		}
	}
}
