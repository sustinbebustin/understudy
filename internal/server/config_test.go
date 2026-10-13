package server_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/server"
)

// The journal setting bounds the events kept in memory and appends every
// event to its file; a capacity the journal refuses fails New.
func TestJournalSettingReachesTheJournal(t *testing.T) {
	file := filepath.Join(t.TempDir(), "journal.ndjson")
	r := launch(t, server.Config{Journal: server.Journal{Capacity: 2, File: file}})
	for _, name := range []string{"a", "b", "c"} {
		mustSQS(t, r.endpoint, "CreateQueue", `{"QueueName":"`+name+`"}`)
	}
	if kept := len(r.srv.Journal().Query(journal.Filter{}).Events); kept > 2 {
		t.Errorf("the journal keeps %d events, want at most its capacity of 2", kept)
	}
	if err := r.stop(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("the journal file: %v", err)
	}
	if lines := bytes.Count(data, []byte("\n")); lines < 3 {
		t.Errorf("the journal file holds %d events, want every CreateQueue:\n%s", lines, data)
	}

	_, err = server.New(server.Config{Journal: server.Journal{Capacity: -1}, Docker: server.DockerDisabled{}, LogOutput: io.Discard})
	if oe, ok := errors.AsType[*journal.OptionsError](err); !ok || oe.Field != "Capacity" {
		t.Fatalf("New with a negative journal capacity = %v, want a journal OptionsError for Capacity", err)
	}
}

// A call whose access key understudy does not know runs in the configured
// default account.
func TestDefaultAccountOwnsUnknownKeys(t *testing.T) {
	account, err := aws.ParseAccountID("111122223333")
	if err != nil {
		t.Fatal(err)
	}
	r := launch(t, server.Config{DefaultAccount: account})
	r.stopOnCleanup(t)
	if out := mustSQS(t, r.endpoint, "CreateQueue", `{"QueueName":"q"}`); !strings.Contains(out, "/111122223333/q") {
		t.Fatalf("CreateQueue with an unknown key = %s, want the queue in 111122223333", out)
	}
}

// ClockScaled starts the clock running at its factor; a factor the clock
// refuses fails New.
func TestClockScaledRunsAtItsFactor(t *testing.T) {
	r := launch(t, server.Config{ClockStart: server.ClockScaled{Factor: 60}})
	r.stopOnCleanup(t)
	c, err := admin.New(r.endpoint, admin.Options{})
	if err != nil {
		t.Fatal(err)
	}
	state, err := c.Clock(t.Context())
	if err != nil || state.Mode != admin.Scaled || state.Factor != 60 {
		t.Fatalf("clock = %+v, %v; want scaled by 60", state, err)
	}

	_, err = server.New(server.Config{ClockStart: server.ClockScaled{Factor: 0}, Docker: server.DockerDisabled{}, LogOutput: io.Discard})
	if se, ok := errors.AsType[*clock.ScaleError](err); !ok || se.Factor != 0 {
		t.Fatalf("New with a zero clock scale = %v, want a ScaleError", err)
	}
}

// A configured snapshot is the state serve starts with; one that cannot be
// read fails New with a SnapshotLoadError naming it.
func TestLoadSnapshotAtStart(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	queue(t, endpoint, "saved", "hello")
	archive := filepath.Join(t.TempDir(), "state.snapshot")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SaveSnapshot(t.Context(), f, admin.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	r := launch(t, server.Config{LoadSnapshot: archive})
	r.stopOnCleanup(t)
	url := r.endpoint + "/000000000000/saved"
	if body := mustSQS(t, r.endpoint, "ReceiveMessage", `{"QueueUrl":"`+url+`"}`); !strings.Contains(body, `"Body":"hello"`) {
		t.Fatalf("ReceiveMessage after loading the snapshot = %s, want the saved message", body)
	}

	missing := filepath.Join(t.TempDir(), "missing.snapshot")
	_, err = server.New(server.Config{LoadSnapshot: missing, Docker: server.DockerDisabled{}, LogOutput: io.Discard})
	if le, ok := errors.AsType[*server.SnapshotLoadError](err); !ok || le.Path != missing || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("New with a missing snapshot = %v, want a SnapshotLoadError for %s", err, missing)
	}
}

// With lambda.network disabled, health reports Lambda degraded to its
// control plane for that reason while Docker answers for the rest, and a
// runtime image pull is refused naming the setting rather than reaching
// Docker.
func TestLambdaNetworkDisabledRunsNoFunction(t *testing.T) {
	mute := filepath.Join(t.TempDir(), "docker.sock")
	if err := os.WriteFile(mute, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ := adminServer(t, server.Config{Docker: server.DockerSocket{Path: mute}, LambdaNetwork: server.NetworkDisabled{}})
	h, err := c.Health(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s := h.Services["lambda"]; s.Status != admin.Degraded || !strings.Contains(s.Reason, "control plane only") || !strings.Contains(s.Reason, "lambda.network is disabled") {
		t.Errorf("lambda = %+v, want degraded to its control plane naming lambda.network", s)
	}
	if s := h.Services["dynamodb"]; strings.Contains(s.Reason, "lambda.network") {
		t.Errorf("dynamodb = %+v, want it untouched by lambda.network", s)
	}
	_, err = c.PullImages(t.Context(), admin.ImagePullRequest{})
	if e := adminErr(t, err, admin.Unavailable); !strings.Contains(e.Message, "lambda.network is disabled") {
		t.Fatalf("PullImages with lambda.network disabled = %+v, want a failure naming the setting", e)
	}
}
