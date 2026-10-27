package authz_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
)

// noRecord are the switches no recording can settle, each with its reason
// in iam-evaluation.md 12.3: AWS's aws:SecureTransport is always true, so
// a recording cannot tell assume-tls from listener.
var noRecord = map[string]bool{"iam.context.secure-transport": true}

// Every evaluation switch names the recording queue item that settles it
// (iam-evaluation.md 13, iam-enforcement-surfaces.md 13), except the
// documented exemptions, which give the reason in Settles instead.
func TestSwitchesNameTheirRecordingItem(t *testing.T) {
	entries := append(authz.Switches(), authz.ResolveFirstSwitch("iam", nil))
	for _, e := range entries {
		switch {
		case noRecord[e.Name] && e.Record != "":
			t.Errorf("switch %s is exempt from a recording item but names %s", e.Name, e.Record)
		case noRecord[e.Name] && e.Settles == nil:
			t.Errorf("switch %s names no recording item and says nothing of why", e.Name)
		case !noRecord[e.Name] && e.Record == "":
			t.Errorf("switch %s names no recording item", e.Name)
		}
	}
}
