package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/journal"
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
	if fe, ok := errors.AsType[*FaultRuleError](err); !ok || fe.Index != 0 {
		t.Fatalf("wire with an invalid fault rule = %v, want a FaultRuleError", err)
	}
	if _, err := w.journal.Append(journal.Event{Detail: journal.TimerFired{Key: clock.TimerKey{Kind: "test.probe", ID: "1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := w.journal.FileErr(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("journal file after a failed wiring: %v, want it closed", err)
	}
}
